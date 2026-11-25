package transport

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"

	"golang.org/x/net/ipv4"
)

// multicastFrameCipher returns the cipher for multicast datagrams: the
// per-session key handed out by an authenticated teacher, else the static key.
func (m *Manager) multicastFrameCipher() *FrameCipher {
	if c := m.mcastCipher.Load(); c != nil {
		return c
	}
	return m.cipher
}

func (m *Manager) startMulticastReceiver(addrStr string) error {
	return m.startMulticastReceiverWithNetwork(addrStr, m.NetworkConfig())
}

func (m *Manager) startMulticastReceiverWithNetwork(addrStr string, network NetworkConfig) error {
	m.mcastStartMu.Lock()
	defer m.mcastStartMu.Unlock()
	m.mu.RLock()
	alreadyStarted := m.mcastRxConn != nil
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return ErrConnClosed
	}
	if alreadyStarted {
		return nil
	}
	udpAddr, err := net.ResolveUDPAddr("udp4", addrStr)
	if err != nil {
		return err
	}
	var pc net.PacketConn
	if udpAddr.IP != nil && udpAddr.IP.IsMulticast() {
		mc, err := listenMulticastAllInterfaces(udpAddr, network)
		if err != nil {
			pc, err = net.ListenPacket("udp4", fmt.Sprintf(":%d", udpAddr.Port))
			if err != nil {
				return err
			}
		} else {
			pc = mc
		}
	} else {
		pc, err = net.ListenPacket("udp4", addrStr)
		if err != nil {
			return err
		}
		if uc, ok := pc.(*net.UDPConn); ok {
			_ = uc.SetReadBuffer(2 * 1024 * 1024)
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = pc.Close()
		return ErrConnClosed
	}
	if m.mcastRxConn != nil {
		m.mu.Unlock()
		_ = pc.Close()
		return nil
	}
	m.mcastRxConn = pc
	m.mcastAddr = addrStr
	m.mu.Unlock()

	go m.multicastReadLoop(pc)
	if m.nackWatcher.CompareAndSwap(false, true) {
		go m.nackWatchLoop()
	}
	if m.networkWatcher.CompareAndSwap(false, true) {
		go m.networkWatchLoop()
	}
	return nil
}

func listenMulticastAllInterfaces(addr *net.UDPAddr, network NetworkConfig) (net.PacketConn, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var conns []*net.UDPConn
	for _, iface := range usableInterfacesWithConfig(ifaces, network) {
		conn, err := net.ListenMulticastUDP("udp4", &iface, addr)
		if err != nil {
			continue
		}
		_ = conn.SetReadBuffer(2 * 1024 * 1024)
		conns = append(conns, conn)
	}
	if len(conns) == 0 {
		return nil, fmt.Errorf("transport: no multicast-capable interface for %s", addr.String())
	}
	if len(conns) == 1 {
		return conns[0], nil
	}
	combined := &multiPacketConn{
		conns:   conns,
		packets: make(chan multiPacket, 64),
		done:    make(chan struct{}),
	}
	for _, conn := range conns {
		go combined.readConn(conn)
	}
	return combined, nil
}

func (m *Manager) restartMulticastReceiver() {
	m.mu.Lock()
	addr := m.mcastAddr
	conn := m.mcastRxConn
	m.mcastRxConn = nil
	m.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	if addr != "" {
		_ = m.startMulticastReceiver(addr)
	}
}

type multiPacketConn struct {
	conns   []*net.UDPConn
	packets chan multiPacket
	done    chan struct{}
	once    sync.Once
}

func (m *Manager) multicastReadLoop(pc net.PacketConn) {
	buf := m.mcastPktPool.Get().([]byte)
	defer m.mcastPktPool.Put(buf)
	for {
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if !m.acceptMulticastSource(src) {
			continue
		}
		m.rxBytesTotal.Add(uint64(n))
		pkt := buf[:n]
		if c := m.multicastFrameCipher(); c != nil {
			dec, err := c.Decrypt(buf[:n])
			if err != nil {
				continue
			}
			pkt = dec
		} else if m.secured() {
			// An authenticated student never trusts plaintext multicast.
			continue
		}
		if m.processIncomingMulticastPacket(pkt) && m.role == core.RoleStudent {
			m.maybeSendMulticastAck()
		}
	}
}

// maybeSendMulticastAck periodically re-confirms multicast delivery so the
// teacher's confirmation lease stays fresh while packets keep arriving.
func (m *Manager) maybeSendMulticastAck() {
	now := time.Now().UnixNano()
	last := m.mcastAckAt.Load()
	if last != 0 && now-last < int64(mcastAckInterval) {
		return
	}
	if !m.mcastAckAt.CompareAndSwap(last, now) {
		return
	}
	if err := m.sendMulticastAck(); err != nil {
		m.mcastAckAt.Store(0)
	}
}

// teacherSourceIP returns the IP multicast traffic is expected to originate
// from, or nil when filtering should be disabled (unknown, unspecified or
// loopback teacher: the local multicast source address then varies).
func teacherSourceIP(addr net.Addr) net.IP {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok || udpAddr == nil || udpAddr.IP == nil {
		return nil
	}
	ip := udpAddr.IP
	if ip.IsLoopback() || ip.IsUnspecified() {
		return nil
	}
	return append(net.IP(nil), ip...)
}

// acceptMulticastSource reports whether a multicast datagram from src should
// be processed. Students only accept packets from the teacher they are
// connected to when that teacher's IP is known.
func (m *Manager) acceptMulticastSource(src net.Addr) bool {
	m.mu.RLock()
	expected := m.mcastSourceIP
	m.mu.RUnlock()
	if expected == nil {
		return true
	}
	udpAddr, ok := src.(*net.UDPAddr)
	if !ok || udpAddr == nil {
		return false
	}
	return udpAddr.IP.Equal(expected)
}

// resetMulticastRxState clears per-teacher multicast receive state after a
// (re)connect so sequence tracking and the multicast ack restart cleanly.
func (m *Manager) resetMulticastRxState(sourceIP net.IP) {
	m.mu.Lock()
	m.mcastSourceIP = sourceIP
	m.mu.Unlock()
	m.mcastRxMu.Lock()
	m.resetMulticastSeqLocked()
	m.mcastRxMu.Unlock()
	m.mcastAckAt.Store(0)
}

func (m *Manager) resetMulticastSeqLocked() {
	m.mcastSeenInit = false
	m.mcastExpectedSeq = 0
	m.mcastReasm = make(map[uint64]*mcastReassembly)
	m.mcastDelivered = make(map[uint64]struct{})
	m.nackPending = make(map[uint64]time.Time)
}

// trackMulticastSeqLocked advances the expected sequence for an arriving seq
// and schedules NACKs for bounded gaps. Forward jumps beyond what the sender's
// ring can serve and large backward jumps (sender restart) resync instead of
// NACKing. Caller holds mcastRxMu.
func (m *Manager) trackMulticastSeqLocked(seq uint64, now time.Time) {
	ring := uint64(m.ringSize)
	if ring == 0 {
		ring = defaultRingBufferSize
	}
	if !m.mcastSeenInit {
		m.mcastSeenInit = true
		m.mcastExpectedSeq = seq + 1
		return
	}
	expected := m.mcastExpectedSeq
	switch {
	case seq == expected:
		m.mcastExpectedSeq = seq + 1
	case seq > expected:
		if seq-expected > ring {
			// Too far ahead to recover via NACK; skip the gap.
			for miss := range m.nackPending {
				if miss < seq-ring {
					delete(m.nackPending, miss)
				}
			}
			m.mcastExpectedSeq = seq + 1
			return
		}
		for miss := expected; miss < seq; miss++ {
			if _, exists := m.nackPending[miss]; !exists {
				m.nackPending[miss] = now
			}
		}
		m.mcastExpectedSeq = seq + 1
	default:
		if expected-seq > ring*mcastResyncFactor {
			// Sender restarted (sequence reset); start over from seq.
			m.resetMulticastSeqLocked()
			m.mcastSeenInit = true
			m.mcastExpectedSeq = seq + 1
		}
	}
}

func (m *Manager) sendMulticastAck() error {
	env := protocol.Envelope{
		Type:      protocol.MsgHelloAck,
		RequestID: internalMcastAckReqID,
		Timestamp: time.Now().UnixMilli(),
	}
	m.mu.RLock()
	var target *SmuxSession
	for _, s := range m.sessions {
		target = s
		break
	}
	m.mu.RUnlock()
	if target == nil {
		return ErrNoSession
	}
	return target.SendEnvelope(env)
}

func (m *Manager) processIncomingMulticastPacket(pkt []byte) bool {
	if m.fecDecoder != nil {
		if _, err := DecodeFECPacket(pkt); err == nil {
			block, ready, err := m.fecDecoder.DecodeBlock(pkt)
			if err != nil || !ready || len(block) < 8 {
				return true
			}
			seq := binary.BigEndian.Uint64(block[0:8])
			frame := block[8:]
			m.deliverMulticastFrame(seq, frame)
			return true
		}
	}

	if len(pkt) < mcastHeaderSize || binary.BigEndian.Uint16(pkt[0:2]) != mcastPacketMagic {
		return false
	}
	seq := binary.BigEndian.Uint64(pkt[2:10])
	idx := int(binary.BigEndian.Uint16(pkt[10:12]))
	total := int(binary.BigEndian.Uint16(pkt[12:14]))
	if total <= 0 || total > maxMcastChunks || idx >= total {
		return false
	}
	payload := pkt[mcastHeaderSize:]

	if total == 1 {
		frame := make([]byte, len(payload))
		copy(frame, payload)
		m.deliverMulticastFrame(seq, frame)
		return true
	}

	m.mcastRxMu.Lock()
	now := time.Now()
	if rasm, ok := m.mcastReasm[seq]; ok && (total != rasm.total || idx >= len(rasm.chunks)) {
		// Inconsistent with the first chunk seen for this seq: drop it.
		m.mcastRxMu.Unlock()
		return false
	}
	m.trackMulticastSeqLocked(seq, now)
	// Look up again: trackMulticastSeqLocked may reset state on resync.
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
				m.evictOlderIncompleteLocked(seq, now)
			}
		}
	}
	m.mcastRxMu.Unlock()

	if completed != nil {
		m.emitMulticastFrame(completed)
	}
	return true
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

func (m *Manager) evictOlderIncompleteLocked(completedSeq uint64, now time.Time) {
	for oldSeq, rasm := range m.mcastReasm {
		if oldSeq < completedSeq && (completedSeq-oldSeq >= 2 || now.Sub(rasm.createdAt) > 12*time.Millisecond) {
			delete(m.mcastReasm, oldSeq)
			if _, dup := m.mcastDelivered[oldSeq]; !dup {
				if _, exists := m.nackPending[oldSeq]; !exists {
					m.nackPending[oldSeq] = now
				}
			}
		}
	}
	if len(m.mcastReasm) > m.ringSize {
		cutoff := uint64(0)
		if completedSeq > uint64(m.ringSize) {
			cutoff = completedSeq - uint64(m.ringSize)
		}
		for oldSeq := range m.mcastReasm {
			if oldSeq < cutoff {
				delete(m.mcastReasm, oldSeq)
			}
		}
	}
}

func (m *Manager) deliverMulticastFrame(seq uint64, frame []byte) {
	m.mcastRxMu.Lock()
	now := time.Now()
	m.trackMulticastSeqLocked(seq, now)
	delete(m.nackPending, seq)
	delete(m.mcastReasm, seq)
	if _, dup := m.mcastDelivered[seq]; dup {
		m.mcastRxMu.Unlock()
		return
	}
	m.recordDeliveredLocked(seq)
	m.evictOlderIncompleteLocked(seq, now)
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
	defer m.nackWatcher.Store(false)
	ticker := time.NewTicker(10 * time.Millisecond)
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
				if now.Sub(rasm.createdAt) > 25*time.Millisecond {
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
	unconfirmed := m.unconfirmedSessionsLocked()
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

	if !m.multicastEnabled.Load() || mcastConn == nil || mcastTarget == nil {
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
		if len(unconfirmed) > 0 {
			m.enqueueFallback(unconfirmed, seq, stored)
		}
		return nil
	}

	total := (len(stored) + defaultMcastChunkSize - 1) / defaultMcastChunkSize
	if total == 0 {
		total = 1
	}
	m.mcastTxMu.Lock()
	pktBuf := m.mcastTxBuf
	for idx := 0; idx < total; idx++ {
		start := idx * defaultMcastChunkSize
		end := start + defaultMcastChunkSize
		if end > len(stored) {
			end = len(stored)
		}
		pktLen := mcastHeaderSize + (end - start)
		pkt := pktBuf[:pktLen]
		binary.BigEndian.PutUint16(pkt[0:2], mcastPacketMagic)
		binary.BigEndian.PutUint64(pkt[2:10], seq)
		binary.BigEndian.PutUint16(pkt[10:12], uint16(idx))
		binary.BigEndian.PutUint16(pkt[12:14], uint16(total))
		copy(pkt[mcastHeaderSize:], stored[start:end])
		if err := m.writeMulticastPacket(mcastConn, mcastTarget, pkt); err != nil {
			m.mcastTxMu.Unlock()
			return err
		}
		if idx > 0 && idx%16 == 0 && m.mcastLimiter == nil {
			runtime.Gosched()
		}
	}
	m.mcastTxMu.Unlock()
	if len(unconfirmed) > 0 {
		m.enqueueFallback(unconfirmed, seq, stored)
	}
	return nil
}

func (m *Manager) enqueueFallback(targets []*SmuxSession, seq uint64, data []byte) {
	select {
	case m.fallbackCh <- fallbackTask{targets: targets, seq: seq, data: data}:
	default:
		select {
		case <-m.fallbackCh:
		default:
		}
		select {
		case m.fallbackCh <- fallbackTask{targets: targets, seq: seq, data: data}:
		default:
		}
	}
}

func (m *Manager) writeMulticastPacket(conn *net.UDPConn, target *net.UDPAddr, pkt []byte) error {
	out := pkt
	if c := m.multicastFrameCipher(); c != nil {
		enc, err := c.Encrypt(pkt)
		if err != nil {
			return err
		}
		out = enc
	}
	if m.mcastLimiter != nil && m.ctx != nil {
		_ = m.mcastLimiter.WaitN(m.ctx, len(out))
	}
	senders := m.multicastSenders()
	if len(senders) == 0 {
		senders = []*net.UDPConn{conn}
	}
	var firstErr error
	sent := false
	for _, sender := range senders {
		n, err := sender.WriteToUDP(out, target)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent = true
		m.txBytesTotal.Add(uint64(n))
		m.txPacketsTotal.Add(1)
	}
	if !sent {
		return firstErr
	}
	return nil
}

// multicastSenders returns one UDP socket per usable LAN interface, each
// pinned with IP_MULTICAST_IF. Without this the OS sends multicast only via
// the default route, which on machines running a VPN/TUN adapter is the
// virtual interface, so classroom students never receive the broadcast.
func (m *Manager) multicastSenders() []*net.UDPConn {
	m.mcastSendersMu.Lock()
	defer m.mcastSendersMu.Unlock()
	if m.mcastSenders != nil && time.Since(m.mcastSendersAt) < mcastSenderRefresh {
		return m.mcastSenders
	}
	ifaces, err := interfaceEnumerator()
	if err != nil {
		m.mcastSendersAt = time.Now()
		return m.mcastSenders
	}
	var next []*net.UDPConn
	for _, iface := range usableInterfacesWithConfig(ifaces, m.NetworkConfig()) {
		if iface.Flags&net.FlagMulticast == 0 || !interfaceHasRoutableIPv4(iface) {
			continue
		}
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			continue
		}
		pc := ipv4.NewPacketConn(conn)
		ifaceCopy := iface
		if err := pc.SetMulticastInterface(&ifaceCopy); err != nil {
			_ = conn.Close()
			continue
		}
		_ = pc.SetMulticastTTL(1)
		_ = pc.SetMulticastLoopback(true)
		_ = conn.SetWriteBuffer(2 * 1024 * 1024)
		next = append(next, conn)
	}
	for _, old := range m.mcastSenders {
		_ = old.Close()
	}
	m.mcastSenders = next
	m.mcastSendersAt = time.Now()
	return next
}

func (m *Manager) closeMulticastSenders() {
	m.mcastSendersMu.Lock()
	defer m.mcastSendersMu.Unlock()
	for _, conn := range m.mcastSenders {
		_ = conn.Close()
	}
	m.mcastSenders = nil
	m.mcastSendersAt = time.Time{}
}

// interfaceHasRoutableIPv4 skips adapters that only carry link-local
// (169.254/16) addresses, i.e. unplugged or unconfigured NICs.
func interfaceHasRoutableIPv4(iface net.Interface) bool {
	addrs, err := interfaceAddressEnumerator(iface)
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 != nil && !ip4.IsLinkLocalUnicast() && !ip4.IsLoopback() {
			return true
		}
	}
	return false
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

func (m *Manager) unconfirmedSessionsLocked() []*SmuxSession {
	var out []*SmuxSession
	now := time.Now()
	for _, s := range m.sessions {
		if at, ok := m.mcastConfirmed[s]; !ok || now.Sub(at) > mcastAckLease {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) sendFallbackToSessions(targets []*SmuxSession, seq uint64, data []byte) error {
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
	var firstErr error
	for _, s := range targets {
		if err := s.SendEnvelope(env); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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

func (m *Manager) serveNACK(sess core.Session, seqs []uint64) {
	for i, seq := range seqs {
		if data, ok := m.LookupRingFrame(seq); ok {
			payload, err := json.Marshal(mediaFallbackPayload{Seq: seq, Data: data})
			if err == nil {
				_ = sess.SendEnvelope(protocol.Envelope{
					Type:      protocol.MsgVideoFrame,
					RequestID: internalFallbackReqID,
					Timestamp: time.Now().UnixMilli(),
					Payload:   payload,
				})
				if i+1 < len(seqs) {
					time.Sleep(5 * time.Millisecond)
				}
			}
		}
	}
}

func (m *multiPacketConn) SetDeadline(t time.Time) error {
	for _, conn := range m.conns {
		if err := conn.SetReadDeadline(t); err != nil {
			return err
		}
	}
	return nil
}

func (m *multiPacketConn) SetReadDeadline(t time.Time) error {
	return m.SetDeadline(t)
}

func (m *multiPacketConn) SetWriteDeadline(t time.Time) error {
	for _, conn := range m.conns {
		if err := conn.SetWriteDeadline(t); err != nil {
			return err
		}
	}
	return nil
}

func (m *multiPacketConn) readConn(conn *net.UDPConn) {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		packet := multiPacket{data: append([]byte(nil), buf[:n]...), addr: addr}
		select {
		case m.packets <- packet:
		case <-m.done:
			return
		}
	}
}

func (m *multiPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case packet := <-m.packets:
		n := copy(p, packet.data)
		return n, packet.addr, nil
	case <-m.done:
		return 0, nil, net.ErrClosed
	}
}

func (m *multiPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	var n int
	var firstErr error
	for _, conn := range m.conns {
		written, err := conn.WriteToUDP(p, addr.(*net.UDPAddr))
		if err == nil {
			n = written
		} else if firstErr == nil {
			firstErr = err
		}
	}
	return n, firstErr
}

func (m *multiPacketConn) LocalAddr() net.Addr {
	if len(m.conns) == 0 {
		return &net.UDPAddr{}
	}
	return m.conns[0].LocalAddr()
}

func (m *multiPacketConn) Close() error {
	m.once.Do(func() { close(m.done) })
	var firstErr error
	for _, conn := range m.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
