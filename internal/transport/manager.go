package transport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"

	"golang.org/x/time/rate"
)

const (
	defaultRingBufferSize = 512
	defaultMcastChunkSize = 1200
	mcastPacketMagic      = uint16(0x4E58) // "NX"
	mcastHeaderSize       = 14             // magic(2) + seq(8) + idx(2) + total(2)
	internalNackRequestID = "__nexlab_mcast_nack__"
	internalFallbackReqID = "__nexlab_mcast_fallback__"
)

var (
	ErrNoSession       = errors.New("transport: session not found")
	ErrNotStarted      = errors.New("transport: manager not started")
	ErrInvalidMcastPkt = errors.New("transport: invalid multicast packet")
)

var (
	_ core.TransportManager = (*Manager)(nil)
	_ core.Session          = (*SmuxSession)(nil)
)

type ManagerOption func(*Manager)

func WithKCPConfig(cfg KCPConfig) ManagerOption {
	return func(m *Manager) {
		m.kcpCfg = cfg
	}
}

func WithDiscoveryConfig(cfg DiscoveryConfig) ManagerOption {
	return func(m *Manager) {
		m.discoveryCfg = cfg
	}
}

func WithTeacherAddr(addr string) ManagerOption {
	return func(m *Manager) {
		m.explicitTeacherAddr = addr
	}
}

func WithMulticastFEC(dataShards, parityShards int) ManagerOption {
	return func(m *Manager) {
		m.fecDataShards = dataShards
		m.fecParityShards = parityShards
	}
}

func WithCipherKey(key []byte) ManagerOption {
	return func(m *Manager) {
		c, err := NewFrameCipher(key)
		if err == nil {
			m.cipher = c
			m.kcpCfg.Cipher = c
		}
	}
}

func WithMulticastRateLimit(bytesPerSec int) ManagerOption {
	return func(m *Manager) {
		if bytesPerSec > 0 {
			m.mcastLimiter = rate.NewLimiter(rate.Limit(bytesPerSec), bytesPerSec)
		}
	}
}

func WithRingBufferSize(size int) ManagerOption {
	return func(m *Manager) {
		if size > 0 {
			m.ringSize = size
		}
	}
}

func WithMulticastListenAddr(addr string) ManagerOption {
	return func(m *Manager) {
		m.explicitMcastListen = addr
	}
}

type mediaNACKPayload struct {
	Seqs []uint64 `json:"seqs"`
}

type mediaFallbackPayload struct {
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data"`
}

type ringEntry struct {
	seq  uint64
	data []byte
}

type mcastReassembly struct {
	total     int
	received  int
	chunks    [][]byte
	createdAt time.Time
}

// Manager implements core.TransportManager for both Teacher and Student roles.
type Manager struct {
	mu                  sync.RWMutex
	role                core.Role
	kcpCfg              KCPConfig
	discoveryCfg        DiscoveryConfig
	explicitTeacherAddr string
	explicitMcastListen string
	cipher              *FrameCipher
	fecDataShards       int
	fecParityShards     int
	fecEncoder          *FECEncoder
	fecDecoder          *FECDecoder
	mcastLimiter        *rate.Limiter

	listener    *KCPListener
	broadcaster *BeaconBroadcaster
	mcastConn   *net.UDPConn
	mcastTarget *net.UDPAddr
	mcastRxConn net.PacketConn

	sessions map[string]*SmuxSession
	aliases  map[string]*SmuxSession

	ringSize int
	ring     []ringEntry
	mcastSeq atomic.Uint64

	onSessionCb   func(core.Session)
	onEnvelopeCb  func(core.Session, protocol.Envelope)
	onMulticastCb func([]byte)

	txBytesTotal     atomic.Uint64
	rxBytesTotal     atomic.Uint64
	txPacketsTotal   atomic.Uint64
	retransTotal     atomic.Uint64
	mcastLossCount   atomic.Uint64
	mcastExpectedSeq uint64
	mcastSeenInit    bool
	mcastReasm       map[uint64]*mcastReassembly
	mcastDelivered   map[uint64]struct{}
	mcastRxMu        sync.Mutex
	nackPending      map[uint64]time.Time

	statsMu       sync.Mutex
	lastStatsTime time.Time
	lastTxBytes   uint64
	lastRxBytes   uint64
	lastTxRate    uint64
	lastRxRate    uint64

	ctx    context.Context
	cancel context.CancelFunc
	closed bool
}

func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		kcpCfg:         DefaultKCPConfig(),
		discoveryCfg:   DefaultDiscoveryConfig(),
		sessions:       make(map[string]*SmuxSession),
		aliases:        make(map[string]*SmuxSession),
		ringSize:       defaultRingBufferSize,
		mcastReasm:     make(map[uint64]*mcastReassembly),
		mcastDelivered: make(map[uint64]struct{}),
		nackPending:    make(map[uint64]time.Time),
		lastStatsTime:  time.Now(),
	}
	for _, opt := range opts {
		opt(m)
	}
	m.ring = make([]ringEntry, m.ringSize)
	if m.fecDataShards > 0 && m.fecParityShards > 0 {
		if enc, err := NewFECEncoder(m.fecDataShards, m.fecParityShards); err == nil {
			m.fecEncoder = enc
			m.fecDecoder = NewFECDecoder()
		}
	}
	return m
}

func (m *Manager) OnSession(fn func(core.Session)) {
	m.mu.Lock()
	m.onSessionCb = fn
	m.mu.Unlock()
}

func (m *Manager) OnEnvelope(fn func(core.Session, protocol.Envelope)) {
	m.mu.Lock()
	m.onEnvelopeCb = fn
	m.mu.Unlock()
}

func (m *Manager) OnMulticastData(fn func([]byte)) {
	m.mu.Lock()
	m.onMulticastCb = fn
	m.mu.Unlock()
}

// StartTeacher starts KCP listener, discovery beacon broadcaster, and optional UDP multicast sender.
func (m *Manager) StartTeacher(ctx context.Context, listenAddr string, beacon protocol.DiscoveryBeacon) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrConnClosed
	}
	m.role = core.RoleTeacher
	m.ctx, m.cancel = context.WithCancel(ctx)

	listener, err := ListenKCP(listenAddr, m.kcpCfg)
	if err != nil {
		return err
	}
	m.listener = listener

	if udpAddr, ok := listener.Addr().(*net.UDPAddr); ok && beacon.KCPPort == 0 {
		beacon.KCPPort = udpAddr.Port
	}
	if beacon.Role == "" {
		beacon.Role = string(core.RoleTeacher)
	}

	if beacon.MulticastIP != "" && beacon.McastPort > 0 {
		target, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(beacon.MulticastIP, strconv.Itoa(beacon.McastPort)))
		if err != nil {
			_ = listener.Close()
			return err
		}
		mcastConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			_ = listener.Close()
			return err
		}
		m.mcastTarget = target
		m.mcastConn = mcastConn
	}

	if m.discoveryCfg.Port > 0 {
		bc, err := StartBeaconBroadcaster(m.ctx, beacon, m.discoveryCfg)
		if err != nil {
			_ = listener.Close()
			if m.mcastConn != nil {
				_ = m.mcastConn.Close()
			}
			return err
		}
		m.broadcaster = bc
	}

	go m.acceptLoop()
	return nil
}

func (m *Manager) acceptLoop() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		sess, err := NewSmuxSession(conn, true, m.handleEnvelope)
		if err != nil {
			_ = conn.Close()
			continue
		}
		sess.SetOnClose(func(closed core.Session) {
			m.removeSession(closed.ID())
		})
		m.addSession(sess)
		m.mu.RLock()
		cb := m.onSessionCb
		m.mu.RUnlock()
		if cb != nil {
			cb(sess)
		}
	}
}

// StartStudent discovers the teacher on channel (or uses explicit addr), connects via KCP+smux, and listens for multicast.
func (m *Manager) StartStudent(ctx context.Context, channel string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrConnClosed
	}
	m.role = core.RoleStudent
	m.ctx, m.cancel = context.WithCancel(ctx)
	teacherAddr := m.explicitTeacherAddr
	dPort := m.discoveryCfg.Port
	explicitMcast := m.explicitMcastListen
	m.mu.Unlock()

	var discovered DiscoveredTeacher
	if teacherAddr == "" {
		listenAddr := fmt.Sprintf(":%d", dPort)
		if dPort <= 0 {
			listenAddr = fmt.Sprintf(":%d", DefaultDiscoveryPort)
		}
		d, err := ListenDiscovery(m.ctx, listenAddr, channel)
		if err != nil {
			return err
		}
		discovered = d
		host := d.Host
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		teacherAddr = net.JoinHostPort(host, strconv.Itoa(d.Beacon.KCPPort))
	}

	kcpConn, err := DialKCP(teacherAddr, m.kcpCfg)
	if err != nil {
		return err
	}
	sess, err := NewSmuxSession(kcpConn, false, m.handleEnvelope)
	if err != nil {
		_ = kcpConn.Close()
		return err
	}
	sess.SetOnClose(func(closed core.Session) {
		m.removeSession(closed.ID())
	})
	m.addSession(sess)

	mcastAddrStr := explicitMcast
	if mcastAddrStr == "" && discovered.Beacon.MulticastIP != "" && discovered.Beacon.McastPort > 0 {
		mcastAddrStr = net.JoinHostPort(discovered.Beacon.MulticastIP, strconv.Itoa(discovered.Beacon.McastPort))
	}
	if mcastAddrStr != "" {
		if err := m.startMulticastReceiver(mcastAddrStr); err != nil {
			_ = err
		}
	}

	m.mu.RLock()
	cb := m.onSessionCb
	m.mu.RUnlock()
	if cb != nil {
		cb(sess)
	}
	return nil
}

func (m *Manager) startMulticastReceiver(addrStr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", addrStr)
	if err != nil {
		return err
	}
	var pc net.PacketConn
	if udpAddr.IP != nil && udpAddr.IP.IsMulticast() {
		mc, err := net.ListenMulticastUDP("udp4", nil, udpAddr)
		if err != nil {
			pc, err = net.ListenPacket("udp4", fmt.Sprintf(":%d", udpAddr.Port))
			if err != nil {
				return err
			}
		} else {
			_ = mc.SetReadBuffer(2 * 1024 * 1024)
			pc = mc
		}
	} else {
		pc, err = net.ListenPacket("udp4", addrStr)
		if err != nil {
			return err
		}
	}

	m.mu.Lock()
	m.mcastRxConn = pc
	m.mu.Unlock()

	go m.multicastReadLoop(pc)
	go m.nackWatchLoop()
	return nil
}

func (m *Manager) multicastReadLoop(pc net.PacketConn) {
	buf := make([]byte, 64*1024)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		m.rxBytesTotal.Add(uint64(n))
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		if m.cipher != nil {
			dec, err := m.cipher.Decrypt(pkt)
			if err != nil {
				continue
			}
			pkt = dec
		}
		m.processIncomingMulticastPacket(pkt)
	}
}

func (m *Manager) processIncomingMulticastPacket(pkt []byte) {
	if m.fecDecoder != nil {
		if _, err := DecodeFECPacket(pkt); err == nil {
			block, ready, err := m.fecDecoder.DecodeBlock(pkt)
			if err != nil || !ready || len(block) < 8 {
				return
			}
			seq := binary.BigEndian.Uint64(block[0:8])
			frame := block[8:]
			m.deliverMulticastFrame(seq, frame)
			return
		}
	}

	if len(pkt) < mcastHeaderSize || binary.BigEndian.Uint16(pkt[0:2]) != mcastPacketMagic {
		return
	}
	seq := binary.BigEndian.Uint64(pkt[2:10])
	idx := int(binary.BigEndian.Uint16(pkt[10:12]))
	total := int(binary.BigEndian.Uint16(pkt[12:14]))
	if total <= 0 || idx >= total {
		return
	}
	payload := pkt[mcastHeaderSize:]

	if total == 1 {
		frame := make([]byte, len(payload))
		copy(frame, payload)
		m.deliverMulticastFrame(seq, frame)
		return
	}

	m.mcastRxMu.Lock()
	now := time.Now()
	if !m.mcastSeenInit {
		m.mcastSeenInit = true
		m.mcastExpectedSeq = seq
	}
	if seq > m.mcastExpectedSeq {
		for miss := m.mcastExpectedSeq; miss < seq; miss++ {
			if _, exists := m.nackPending[miss]; !exists {
				m.nackPending[miss] = now
			}
		}
		m.mcastExpectedSeq = seq + 1
	} else if seq == m.mcastExpectedSeq {
		m.mcastExpectedSeq = seq + 1
	}

	rasm, ok := m.mcastReasm[seq]
	if !ok {
		rasm = &mcastReassembly{
			total:     total,
			chunks:    make([][]byte, total),
			createdAt: now,
		}
		m.mcastReasm[seq] = rasm
	}
	var completed []byte
	if rasm.chunks[idx] == nil {
		c := make([]byte, len(payload))
		copy(c, payload)
		rasm.chunks[idx] = c
		rasm.received++
		if rasm.received == rasm.total {
			totalLen := 0
			for _, ch := range rasm.chunks {
				totalLen += len(ch)
			}
			completed = make([]byte, 0, totalLen)
			for _, ch := range rasm.chunks {
				completed = append(completed, ch...)
			}
			delete(m.mcastReasm, seq)
			delete(m.nackPending, seq)
			if _, dup := m.mcastDelivered[seq]; dup {
				completed = nil
			} else {
				m.recordDeliveredLocked(seq)
			}
		}
	}
	m.mcastRxMu.Unlock()

	if completed != nil {
		m.emitMulticastFrame(completed)
	}
}

func (m *Manager) recordDeliveredLocked(seq uint64) {
	m.mcastDelivered[seq] = struct{}{}
	if len(m.mcastDelivered) > m.ringSize*2 {
		cutoff := uint64(0)
		if seq > uint64(m.ringSize) {
			cutoff = seq - uint64(m.ringSize)
		}
		for k := range m.mcastDelivered {
			if k < cutoff {
				delete(m.mcastDelivered, k)
			}
		}
	}
}

func (m *Manager) deliverMulticastFrame(seq uint64, frame []byte) {
	m.mcastRxMu.Lock()
	now := time.Now()
	if !m.mcastSeenInit {
		m.mcastSeenInit = true
		m.mcastExpectedSeq = seq + 1
	} else if seq > m.mcastExpectedSeq {
		for miss := m.mcastExpectedSeq; miss < seq; miss++ {
			if _, exists := m.nackPending[miss]; !exists {
				m.nackPending[miss] = now
			}
		}
		m.mcastExpectedSeq = seq + 1
	} else if seq == m.mcastExpectedSeq {
		m.mcastExpectedSeq = seq + 1
	}
	delete(m.nackPending, seq)
	delete(m.mcastReasm, seq)
	if _, dup := m.mcastDelivered[seq]; dup {
		m.mcastRxMu.Unlock()
		return
	}
	m.recordDeliveredLocked(seq)
	m.mcastRxMu.Unlock()

	m.emitMulticastFrame(frame)
}

func (m *Manager) emitMulticastFrame(frame []byte) {
	m.mu.RLock()
	cb := m.onMulticastCb
	m.mu.RUnlock()
	if cb != nil {
		cb(frame)
	}
}

func (m *Manager) nackWatchLoop() {
	ticker := time.NewTicker(15 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			var toRequest []uint64
			now := time.Now()
			m.mcastRxMu.Lock()
			for seq, rasm := range m.mcastReasm {
				if now.Sub(rasm.createdAt) > 30*time.Millisecond {
					delete(m.mcastReasm, seq)
					m.nackPending[seq] = now.Add(-20 * time.Millisecond)
				}
			}
			for seq, ts := range m.nackPending {
				if now.Sub(ts) >= 20*time.Millisecond {
					toRequest = append(toRequest, seq)
					delete(m.nackPending, seq)
					m.mcastLossCount.Add(1)
				}
			}
			m.mcastRxMu.Unlock()

			if len(toRequest) > 0 {
				m.sendNACK(toRequest)
			}
		}
	}
}

func (m *Manager) sendNACK(seqs []uint64) {
	payload, err := json.Marshal(mediaNACKPayload{Seqs: seqs})
	if err != nil {
		return
	}
	env := protocol.Envelope{
		Type:      protocol.MsgMediaNACK,
		RequestID: internalNackRequestID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
	m.mu.RLock()
	var target *SmuxSession
	for _, s := range m.sessions {
		target = s
		break
	}
	m.mu.RUnlock()
	if target != nil {
		_ = target.SendEnvelope(env)
	}
}

func (m *Manager) BroadcastMulticast(frame []byte) error {
	m.mu.RLock()
	closed := m.closed
	mcastConn := m.mcastConn
	mcastTarget := m.mcastTarget
	m.mu.RUnlock()
	if closed {
		return ErrConnClosed
	}

	seq := m.mcastSeq.Add(1)
	stored := make([]byte, len(frame))
	copy(stored, frame)

	m.mu.Lock()
	m.ring[int(seq)%m.ringSize] = ringEntry{seq: seq, data: stored}
	m.mu.Unlock()

	if mcastConn == nil || mcastTarget == nil {
		return m.sendFallbackToAll(seq, stored)
	}

	if m.fecEncoder != nil {
		block := make([]byte, 8+len(stored))
		binary.BigEndian.PutUint64(block[0:8], seq)
		copy(block[8:], stored)
		shards, err := m.fecEncoder.EncodeBlock(block)
		if err != nil {
			return err
		}
		for _, shard := range shards {
			if err := m.writeMulticastPacket(mcastConn, mcastTarget, shard); err != nil {
				return err
			}
		}
		return nil
	}

	total := (len(stored) + defaultMcastChunkSize - 1) / defaultMcastChunkSize
	if total == 0 {
		total = 1
	}
	for idx := 0; idx < total; idx++ {
		start := idx * defaultMcastChunkSize
		end := start + defaultMcastChunkSize
		if end > len(stored) {
			end = len(stored)
		}
		pkt := make([]byte, mcastHeaderSize+(end-start))
		binary.BigEndian.PutUint16(pkt[0:2], mcastPacketMagic)
		binary.BigEndian.PutUint64(pkt[2:10], seq)
		binary.BigEndian.PutUint16(pkt[10:12], uint16(idx))
		binary.BigEndian.PutUint16(pkt[12:14], uint16(total))
		copy(pkt[mcastHeaderSize:], stored[start:end])
		if err := m.writeMulticastPacket(mcastConn, mcastTarget, pkt); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) writeMulticastPacket(conn *net.UDPConn, target *net.UDPAddr, pkt []byte) error {
	out := pkt
	if m.cipher != nil {
		enc, err := m.cipher.Encrypt(pkt)
		if err != nil {
			return err
		}
		out = enc
	}
	if m.mcastLimiter != nil && m.ctx != nil {
		_ = m.mcastLimiter.WaitN(m.ctx, len(out))
	}
	n, err := conn.WriteToUDP(out, target)
	if err != nil {
		return err
	}
	m.txBytesTotal.Add(uint64(n))
	m.txPacketsTotal.Add(1)
	return nil
}

func (m *Manager) sendFallbackToAll(seq uint64, data []byte) error {
	payload, err := json.Marshal(mediaFallbackPayload{Seq: seq, Data: data})
	if err != nil {
		return err
	}
	env := protocol.Envelope{
		Type:      protocol.MsgVideoFrame,
		RequestID: internalFallbackReqID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
	return m.BroadcastControl(env, nil)
}

// LookupRingFrame fetches a previously broadcast frame from the teacher's ring buffer.
func (m *Manager) LookupRingFrame(seq uint64) ([]byte, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.ring[int(seq)%m.ringSize]
	if entry.seq != seq || entry.data == nil {
		return nil, false
	}
	cp := make([]byte, len(entry.data))
	copy(cp, entry.data)
	return cp, true
}

func (m *Manager) addSession(s *SmuxSession) {
	m.mu.Lock()
	m.sessions[s.ID()] = s
	m.mu.Unlock()
}

func (m *Manager) removeSession(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	for alias, target := range m.aliases {
		if target == s || alias == id {
			delete(m.aliases, alias)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) findSessionLocked(id string) *SmuxSession {
	if s, ok := m.sessions[id]; ok {
		return s
	}
	if s, ok := m.aliases[id]; ok {
		return s
	}
	return nil
}

func (m *Manager) handleEnvelope(sess core.Session, env protocol.Envelope) {
	if env.SenderID != "" {
		if sm, ok := sess.(*SmuxSession); ok {
			m.mu.Lock()
			m.aliases[env.SenderID] = sm
			m.mu.Unlock()
		}
	}

	if env.Type == protocol.MsgMediaNACK && env.RequestID == internalNackRequestID {
		var req mediaNACKPayload
		if err := json.Unmarshal(env.Payload, &req); err == nil {
			for _, seq := range req.Seqs {
				if data, ok := m.LookupRingFrame(seq); ok {
					payload, err := json.Marshal(mediaFallbackPayload{Seq: seq, Data: data})
					if err == nil {
						_ = sess.SendEnvelope(protocol.Envelope{
							Type:      protocol.MsgVideoFrame,
							RequestID: internalFallbackReqID,
							Timestamp: time.Now().UnixMilli(),
							Payload:   payload,
						})
					}
				}
			}
		}
		return
	}

	if env.Type == protocol.MsgVideoFrame && env.RequestID == internalFallbackReqID {
		var fb mediaFallbackPayload
		if err := json.Unmarshal(env.Payload, &fb); err == nil {
			m.deliverMulticastFrame(fb.Seq, fb.Data)
		}
		return
	}

	m.mu.RLock()
	cb := m.onEnvelopeCb
	m.mu.RUnlock()
	if cb != nil {
		cb(sess, env)
	}
}

func (m *Manager) SendTo(studentID string, env protocol.Envelope) error {
	m.mu.RLock()
	var target *SmuxSession
	if studentID == "" {
		for _, s := range m.sessions {
			target = s
			break
		}
	} else {
		target = m.findSessionLocked(studentID)
	}
	m.mu.RUnlock()

	if target == nil {
		return ErrNoSession
	}
	return target.SendEnvelope(env)
}

func (m *Manager) BroadcastControl(env protocol.Envelope, targetIDs []string) error {
	m.mu.RLock()
	var targets []*SmuxSession
	if len(targetIDs) == 0 {
		targets = make([]*SmuxSession, 0, len(m.sessions))
		for _, s := range m.sessions {
			targets = append(targets, s)
		}
	} else {
		seen := make(map[*SmuxSession]struct{}, len(targetIDs))
		for _, id := range targetIDs {
			if s := m.findSessionLocked(id); s != nil {
				if _, ok := seen[s]; !ok {
					seen[s] = struct{}{}
					targets = append(targets, s)
				}
			}
		}
	}
	m.mu.RUnlock()

	var firstErr error
	for _, s := range targets {
		if err := s.SendEnvelope(env); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) Sessions() []core.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]core.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func (m *Manager) Stats() core.TransportStats {
	m.mu.RLock()
	active := len(m.sessions)
	var kcpTx, kcpRx, kcpTxPkts, kcpRetrans uint64
	for _, s := range m.sessions {
		if ks, ok := s.KCPStats(); ok {
			kcpTx += ks.TxBytes
			kcpRx += ks.RxBytes
			kcpTxPkts += ks.TxPackets
			kcpRetrans += ks.Retransmits
		}
	}
	m.mu.RUnlock()

	totalTx := m.txBytesTotal.Load() + kcpTx
	totalRx := m.rxBytesTotal.Load() + kcpRx
	totalPkts := m.txPacketsTotal.Load() + kcpTxPkts
	totalLoss := m.retransTotal.Load() + kcpRetrans + m.mcastLossCount.Load()

	now := time.Now()
	m.statsMu.Lock()
	elapsed := now.Sub(m.lastStatsTime).Seconds()
	if elapsed >= 0.1 {
		dTx := totalTx - m.lastTxBytes
		dRx := totalRx - m.lastRxBytes
		m.lastTxRate = uint64(float64(dTx) / elapsed)
		m.lastRxRate = uint64(float64(dRx) / elapsed)
		m.lastTxBytes = totalTx
		m.lastRxBytes = totalRx
		m.lastStatsTime = now
	}
	txRate := m.lastTxRate
	rxRate := m.lastRxRate
	m.statsMu.Unlock()

	var lossRate float64
	if totalPkts > 0 {
		lossRate = float64(totalLoss) / float64(totalPkts)
		if lossRate > 1.0 {
			lossRate = 1.0
		}
	}

	return core.TransportStats{
		ActiveSessions: active,
		TxBytesPerSec:  txRate,
		RxBytesPerSec:  rxRate,
		PacketLossRate: lossRate,
	}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	cancel := m.cancel
	listener := m.listener
	broadcaster := m.broadcaster
	mcastConn := m.mcastConn
	mcastRxConn := m.mcastRxConn
	sessions := make([]*SmuxSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = make(map[string]*SmuxSession)
	m.aliases = make(map[string]*SmuxSession)
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if broadcaster != nil {
		_ = broadcaster.Close()
	}
	if mcastConn != nil {
		_ = mcastConn.Close()
	}
	if mcastRxConn != nil {
		_ = mcastRxConn.Close()
	}
	for _, s := range sessions {
		_ = s.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
	return nil
}
