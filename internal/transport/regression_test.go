package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"testing"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
)

func buildMcastPacket(seq uint64, idx, total int, payload []byte) []byte {
	pkt := make([]byte, mcastHeaderSize+len(payload))
	binary.BigEndian.PutUint16(pkt[0:2], mcastPacketMagic)
	binary.BigEndian.PutUint64(pkt[2:10], seq)
	binary.BigEndian.PutUint16(pkt[10:12], uint16(idx))
	binary.BigEndian.PutUint16(pkt[12:14], uint16(total))
	copy(pkt[mcastHeaderSize:], payload)
	return pkt
}

func TestMulticastReassemblyRejectsInconsistentChunks(t *testing.T) {
	m := NewManager()
	var frames [][]byte
	m.OnMulticastData(func(frame []byte) { frames = append(frames, frame) })

	if !m.processIncomingMulticastPacket(buildMcastPacket(5, 0, 2, []byte("ab"))) {
		t.Fatal("first chunk rejected")
	}
	// Same seq, larger total and an index beyond the original chunk slice:
	// previously indexed out of range and panicked.
	if m.processIncomingMulticastPacket(buildMcastPacket(5, 7, 10, []byte("zz"))) {
		t.Fatal("inconsistent chunk accepted")
	}
	if m.processIncomingMulticastPacket(buildMcastPacket(6, 0, maxMcastChunks+1, []byte("x"))) {
		t.Fatal("oversized total accepted")
	}
	if !m.processIncomingMulticastPacket(buildMcastPacket(5, 1, 2, []byte("cd"))) {
		t.Fatal("second chunk rejected")
	}
	if len(frames) != 1 || string(frames[0]) != "abcd" {
		t.Fatalf("frames = %q, want [abcd]", frames)
	}
}

func TestScheduleStudentReconnectStaleGenerationReleasesLock(t *testing.T) {
	m := NewManager()
	gen := m.nextConnectionGeneration()
	m.invalidateConnectionGeneration()
	m.scheduleStudentReconnect(gen)

	locked := make(chan struct{})
	go func() {
		m.mu.Lock()
		m.mu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("m.mu still held after stale scheduleStudentReconnect")
	}
}

func TestMulticastSeqGapIsBounded(t *testing.T) {
	m := NewManager()
	now := time.Now()
	m.mcastRxMu.Lock()
	defer m.mcastRxMu.Unlock()

	m.trackMulticastSeqLocked(10, now)
	m.trackMulticastSeqLocked(13, now)
	if len(m.nackPending) != 2 {
		t.Fatalf("small gap: nackPending = %d, want 2", len(m.nackPending))
	}

	// A huge forward jump (or one near the uint64 limit) must resync without
	// enumerating the gap.
	m.trackMulticastSeqLocked(1<<62, now)
	if len(m.nackPending) > m.ringSize {
		t.Fatalf("huge gap queued %d NACKs", len(m.nackPending))
	}
	if m.mcastExpectedSeq != 1<<62+1 {
		t.Fatalf("expected seq = %d after resync", m.mcastExpectedSeq)
	}
	m.trackMulticastSeqLocked(math.MaxUint64, now)
	m.trackMulticastSeqLocked(3, now)
	if len(m.nackPending) > m.ringSize {
		t.Fatalf("wrap queued %d NACKs", len(m.nackPending))
	}

	// A sender restart (seq far below expected) resyncs rx state.
	m.trackMulticastSeqLocked(1000, now)
	m.mcastDelivered[1] = struct{}{}
	m.trackMulticastSeqLocked(1, now)
	if m.mcastExpectedSeq != 2 {
		t.Fatalf("restart resync expected seq = %d, want 2", m.mcastExpectedSeq)
	}
	if _, stale := m.mcastDelivered[1]; stale {
		t.Fatal("delivered set not cleared on restart resync")
	}
}

func TestFECStreamDecodeRejectsInconsistentGroup(t *testing.T) {
	d := NewFECDecoder()
	first := EncodeFECPacket(FECPacket{GroupID: 1, Index: 0, Total: 4, DataNum: 2, Payload: []byte{0, 1, 'a'}})
	if _, err := d.DecodeStreamPacket(first); err != nil {
		t.Fatalf("first packet: %v", err)
	}
	bad := EncodeFECPacket(FECPacket{GroupID: 1, Index: 8, Total: 10, DataNum: 2, Payload: []byte{0, 1, 'b'}})
	if _, err := d.DecodeStreamPacket(bad); !errors.Is(err, ErrFECInvalidPacket) {
		t.Fatalf("inconsistent packet err = %v, want ErrFECInvalidPacket", err)
	}
	if _, _, err := d.DecodeBlock(bad); !errors.Is(err, ErrFECInvalidPacket) {
		t.Fatalf("DecodeBlock inconsistent err = %v", err)
	}
	huge := EncodeFECPacket(FECPacket{GroupID: 2, Index: 0, Total: fecMaxShards + 1, DataNum: 1, Payload: []byte{0}})
	if _, err := d.DecodeStreamPacket(huge); !errors.Is(err, ErrFECInvalidPacket) {
		t.Fatalf("oversized Total err = %v", err)
	}
	for gid := uint32(100); gid < 100+4*fecMaxGroups; gid++ {
		pkt := EncodeFECPacket(FECPacket{GroupID: gid, Index: 0, Total: 4, DataNum: 2, Payload: []byte{0, 1, 'x'}})
		if _, err := d.DecodeStreamPacket(pkt); err != nil {
			t.Fatalf("group %d: %v", gid, err)
		}
	}
	if n := len(d.groups); n > fecMaxGroups {
		t.Fatalf("live groups = %d, want <= %d", n, fecMaxGroups)
	}
}

func TestStartTeacherFailureCanRetry(t *testing.T) {
	previous := listenUDPFunc
	defer func() { listenUDPFunc = previous }()
	injected := errors.New("injected listen failure")
	listenUDPFunc = func(string, *net.UDPAddr) (*net.UDPConn, error) { return nil, injected }

	m := NewManager(WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beacon := protocol.DiscoveryBeacon{Channel: "retry", MulticastIP: "239.255.42.98", McastPort: 39198}

	if err := m.StartTeacher(ctx, "127.0.0.1:0", beacon); !errors.Is(err, injected) {
		t.Fatalf("first StartTeacher err = %v, want injected", err)
	}
	m.mu.RLock()
	starting, started, leftCtx, leftListener := m.starting, m.started, m.ctx, m.listener
	m.mu.RUnlock()
	if starting || started || leftCtx != nil || leftListener != nil {
		t.Fatalf("state not cleaned: starting=%v started=%v ctx=%v listener=%v", starting, started, leftCtx, leftListener)
	}

	listenUDPFunc = previous
	if err := m.StartTeacher(ctx, "127.0.0.1:0", beacon); err != nil {
		t.Fatalf("retry StartTeacher: %v", err)
	}
}

func TestInvalidCipherKeyFailsStart(t *testing.T) {
	m := NewManager(WithCipherKey([]byte("short")), WithDiscoveryConfig(DiscoveryConfig{Port: 0}))
	defer m.Close()
	if err := m.StartTeacher(context.Background(), "127.0.0.1:0", protocol.DiscoveryBeacon{}); !errors.Is(err, ErrCipherKeyLength) {
		t.Fatalf("StartTeacher err = %v, want ErrCipherKeyLength", err)
	}
	if err := m.StartStudent(context.Background(), "x"); !errors.Is(err, ErrCipherKeyLength) {
		t.Fatalf("StartStudent err = %v, want ErrCipherKeyLength", err)
	}
}

func rawKCPSegment(conv uint32, cmd uint8, sn uint32, data []byte) []byte {
	buf := make([]byte, int(IKCP_OVERHEAD)+len(data))
	binary.LittleEndian.PutUint32(buf[0:4], conv)
	buf[4] = cmd
	binary.LittleEndian.PutUint16(buf[6:8], 128)
	binary.LittleEndian.PutUint32(buf[12:16], sn)
	binary.LittleEndian.PutUint32(buf[20:24], uint32(len(data)))
	copy(buf[24:], data)
	return buf
}

func TestKCPListenerOnlyAcceptsInitialSegments(t *testing.T) {
	l, err := ListenKCP("127.0.0.1:0", DefaultKCPConfig())
	if err != nil {
		t.Fatalf("ListenKCP: %v", err)
	}
	defer l.Close()
	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client socket: %v", err)
	}
	defer client.Close()
	addr := l.Addr()

	// Stray non-initial segments must not create sessions.
	_, _ = client.WriteTo(rawKCPSegment(7, IKCP_CMD_ACK, 0, nil), addr)
	_, _ = client.WriteTo(rawKCPSegment(8, IKCP_CMD_PUSH, 5, []byte("late")), addr)
	// A real first segment does.
	_, _ = client.WriteTo(rawKCPSegment(9, IKCP_CMD_PUSH, 0, []byte("hi")), addr)

	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			accepted <- c
		}
	}()
	var conn net.Conn
	select {
	case conn = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial segment did not produce a session")
	}
	l.mu.Lock()
	n := len(l.sessions)
	l.mu.Unlock()
	if n != 1 {
		t.Fatalf("sessions = %d, want 1 (stray packets must be ignored)", n)
	}

	// After close, the same raddr#conv is tombstoned.
	key := client.LocalAddr().String() + "#9"
	_ = conn.Close()
	l.mu.Lock()
	_, tomb := l.tombstones[key]
	_, live := l.sessions[key]
	l.mu.Unlock()
	if !tomb || live {
		t.Fatalf("closed key tombstone=%v live=%v", tomb, live)
	}
	if l.cfg.Cipher != nil {
		t.Fatal("unexpected cipher")
	}
}

func TestSmuxSessionSetOnCloseAfterClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	sess, err := NewSmuxSession(a, true, nil)
	if err != nil {
		t.Fatalf("NewSmuxSession: %v", err)
	}
	_ = sess.Close()
	fired := make(chan struct{}, 1)
	sess.SetOnClose(func(core.Session) { fired <- struct{}{} })
	select {
	case <-fired:
	default:
		t.Fatal("onClose registered after Close was not invoked")
	}
}

func newPipeSession(t *testing.T) *SmuxSession {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { _ = b.Close() })
	sess, err := NewSmuxSession(a, true, nil)
	if err != nil {
		t.Fatalf("NewSmuxSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func TestAliasNotHijackedByOtherSession(t *testing.T) {
	m := NewManager()
	owner := newPipeSession(t)
	intruder := newPipeSession(t)
	m.addSession(owner)
	m.addSession(intruder)
	env := protocol.Envelope{Type: protocol.MsgPing, SenderID: "stu-1"}

	m.handleEnvelope(owner, env)
	m.handleEnvelope(intruder, env)
	m.mu.RLock()
	got := m.aliases["stu-1"]
	m.mu.RUnlock()
	if got != owner {
		t.Fatal("alias rebound to a different session")
	}

	_ = owner.Close()
	m.handleEnvelope(intruder, env)
	m.mu.RLock()
	got = m.aliases["stu-1"]
	m.mu.RUnlock()
	if got != intruder {
		t.Fatal("alias not rebound after owner closed")
	}
}

func TestAddSessionAfterCloseIsRejected(t *testing.T) {
	m := NewManager()
	gen := m.nextConnectionGeneration()
	_ = m.Close()
	sess := newPipeSession(t)
	if m.addSessionIfCurrent(sess, gen) {
		t.Fatal("session added after Close")
	}
	if len(m.Sessions()) != 0 {
		t.Fatal("closed manager holds sessions")
	}
}
