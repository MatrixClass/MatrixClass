package transport

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"

	"github.com/xtaci/smux"
)

const (
	defaultKCPMTU      = 1350
	defaultKCPSndWnd   = 1024
	defaultKCPRcvWnd   = 1024
	defaultKCPInterval = 10
	// kcpTombstoneTTL is how long a closed raddr#conv key is refused so late
	// or retransmitted packets of a dead connection cannot resurrect it.
	kcpTombstoneTTL    = 5 * time.Second
	streamTagEnvelope  = byte(0xFE)
	streamTagMediaEnv  = byte(0xFD)
	ctrlWriteTimeout   = 1500 * time.Millisecond
	mediaWriteTimeout  = 3 * time.Second
	mediaQueueCapacity = 8
	audioQueueCapacity = 4
)

var (
	ErrConnClosed     = errors.New("transport: connection closed")
	ErrListenerClosed = errors.New("transport: listener closed")
	ErrReadTimeout    = &netTimeoutError{msg: "i/o timeout"}
	ErrWriteTimeout   = &netTimeoutError{msg: "i/o timeout"}
)

type netTimeoutError struct {
	msg string
}

func (e *netTimeoutError) Error() string   { return e.msg }
func (e *netTimeoutError) Timeout() bool   { return true }
func (e *netTimeoutError) Temporary() bool { return true }

// KCPConfig configures KCPConn and SmuxSession parameters.
type KCPConfig struct {
	Conv         uint32
	MTU          int
	SndWnd       int
	RcvWnd       int
	NoDelay      int
	Interval     int
	FastResend   int
	NoCwnd       int
	Cipher       *FrameCipher
	DataShards   int
	ParityShards int
}

func DefaultKCPConfig() KCPConfig {
	return KCPConfig{
		MTU:        defaultKCPMTU,
		SndWnd:     defaultKCPSndWnd,
		RcvWnd:     defaultKCPRcvWnd,
		NoDelay:    1,
		Interval:   defaultKCPInterval,
		FastResend: 2,
		NoCwnd:     1,
	}
}

// KCPConn wraps a KCP state machine over a UDP PacketConn to implement net.Conn.
type KCPConn struct {
	kcp        *KCP
	udpConn    net.PacketConn
	remoteAddr net.Addr
	localAddr  net.Addr
	ownsConn   bool
	cipher     *FrameCipher

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	closed        bool
	closeOnce     sync.Once
	done          chan struct{}

	chReadEvent  chan struct{}
	chWriteEvent chan struct{}
	chUpdate     chan struct{}
	interval     time.Duration
	onClose      func()

	startTime time.Time
}

func newKCPConn(conv uint32, udpConn net.PacketConn, remoteAddr net.Addr, ownsConn bool, cfg KCPConfig, onClose func()) *KCPConn {
	if cfg.MTU <= 0 {
		cfg.MTU = defaultKCPMTU
	}
	if cfg.SndWnd <= 0 {
		cfg.SndWnd = defaultKCPSndWnd
	}
	if cfg.RcvWnd <= 0 {
		cfg.RcvWnd = defaultKCPRcvWnd
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultKCPInterval
	}

	kc := &KCPConn{
		udpConn:      udpConn,
		remoteAddr:   remoteAddr,
		localAddr:    udpConn.LocalAddr(),
		ownsConn:     ownsConn,
		cipher:       cfg.Cipher,
		done:         make(chan struct{}),
		chReadEvent:  make(chan struct{}, 1),
		chWriteEvent: make(chan struct{}, 1),
		chUpdate:     make(chan struct{}, 1),
		interval:     time.Duration(cfg.Interval) * time.Millisecond,
		onClose:      onClose,
		startTime:    time.Now(),
	}

	kcp := NewKCP(conv, func(buf []byte, size int) {
		if size <= 0 {
			return
		}
		pkt := make([]byte, size)
		copy(pkt, buf[:size])
		if kc.cipher != nil {
			enc, err := kc.cipher.Encrypt(pkt)
			if err != nil {
				return
			}
			pkt = enc
		}
		_, _ = kc.udpConn.WriteTo(pkt, kc.remoteAddr)
	})
	_ = kcp.SetMTU(cfg.MTU)
	kcp.WndSize(cfg.SndWnd, cfg.RcvWnd)
	kcp.NoDelay(cfg.NoDelay, cfg.Interval, cfg.FastResend, cfg.NoCwnd)
	kcp.SetStreamMode(true)
	kc.kcp = kcp

	go kc.updateLoop()
	return kc
}

func (c *KCPConn) currentMS() uint32 {
	return uint32(time.Since(c.startTime).Milliseconds())
}

func signalChan(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *KCPConn) updateLoop() {
	interval := c.interval
	if interval <= 0 {
		interval = time.Duration(defaultKCPInterval) * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.kcp.Update(c.currentMS())
	for {
		select {
		case <-c.done:
			return
		case <-c.chUpdate:
			c.kcp.Update(c.currentMS())
			c.kcp.Flush()
		case <-ticker.C:
			c.kcp.Update(c.currentMS())
		}
	}
}

func (c *KCPConn) input(pkt []byte) error {
	if c.cipher != nil {
		dec, err := c.cipher.Decrypt(pkt)
		if err != nil {
			return err
		}
		pkt = dec
	}
	return c.inputPlain(pkt)
}

// inputPlain feeds an already-decrypted datagram into the KCP state machine.
func (c *KCPConn) inputPlain(pkt []byte) error {
	if err := c.kcp.Input(pkt); err != nil {
		return err
	}
	if c.kcp.PeekSize() >= 0 {
		signalChan(c.chReadEvent)
	}
	signalChan(c.chWriteEvent)
	signalChan(c.chUpdate)
	return nil
}

func (c *KCPConn) Read(b []byte) (int, error) {
	for {
		c.mu.Lock()
		closed := c.closed
		deadline := c.readDeadline
		c.mu.Unlock()
		if closed {
			return 0, io.EOF
		}

		n, err := c.kcp.Recv(b)
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return n, nil
		}

		var timer *time.Timer
		var timeoutCh <-chan time.Time
		if !deadline.IsZero() {
			wait := time.Until(deadline)
			if wait <= 0 {
				return 0, ErrReadTimeout
			}
			timer = time.NewTimer(wait)
			timeoutCh = timer.C
		}

		select {
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, io.EOF
		case <-c.chReadEvent:
			if timer != nil {
				timer.Stop()
			}
		case <-timeoutCh:
			return 0, ErrReadTimeout
		}
	}
}

func (c *KCPConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	written := 0
	const maxChunk = 16 * 1024
	for written < len(b) {
		c.mu.Lock()
		closed := c.closed
		deadline := c.writeDeadline
		c.mu.Unlock()
		if closed {
			return written, ErrConnClosed
		}

		if c.kcp.WaitSnd() >= int(c.kcp.sndWnd)*2 {
			var timer *time.Timer
			var timeoutCh <-chan time.Time
			if !deadline.IsZero() {
				wait := time.Until(deadline)
				if wait <= 0 {
					return written, ErrWriteTimeout
				}
				timer = time.NewTimer(wait)
				timeoutCh = timer.C
			}
			select {
			case <-c.done:
				if timer != nil {
					timer.Stop()
				}
				return written, ErrConnClosed
			case <-c.chWriteEvent:
				if timer != nil {
					timer.Stop()
				}
			case <-timeoutCh:
				return written, ErrWriteTimeout
			}
			continue
		}

		end := written + maxChunk
		if end > len(b) {
			end = len(b)
		}
		n, err := c.kcp.Send(b[written:end])
		if err != nil {
			return written, err
		}
		written += n
		signalChan(c.chUpdate)
	}
	return written, nil
}

func (c *KCPConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.done)
		if c.ownsConn && c.udpConn != nil {
			err = c.udpConn.Close()
		}
		if c.onClose != nil {
			c.onClose()
		}
	})
	return err
}

func (c *KCPConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *KCPConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *KCPConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *KCPConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *KCPConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

func (c *KCPConn) Stats() KCPStats {
	return c.kcp.Stats()
}

// KCPListener accepts incoming KCP connections multiplexed over a single UDP socket.
type KCPListener struct {
	udpConn    net.PacketConn
	cfg        KCPConfig
	mu         sync.Mutex
	sessions   map[string]*KCPConn
	tombstones map[string]time.Time
	lastSweep  time.Time
	acceptCh   chan *KCPConn
	done       chan struct{}
	closed     bool
}

func ListenKCP(addr string, cfg KCPConfig) (*KCPListener, error) {
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return nil, err
	}
	return ServeKCP(pc, cfg), nil
}

func ServeKCP(pc net.PacketConn, cfg KCPConfig) *KCPListener {
	l := &KCPListener{
		udpConn:    pc,
		cfg:        cfg,
		sessions:   make(map[string]*KCPConn),
		tombstones: make(map[string]time.Time),
		acceptCh:   make(chan *KCPConn, 128),
		done:       make(chan struct{}),
	}
	go l.readLoop()
	return l
}

func (l *KCPListener) readLoop() {
	buf := make([]byte, 64*1024)
	for {
		n, raddr, err := l.udpConn.ReadFrom(buf)
		if err != nil {
			select {
			case <-l.done:
				return
			default:
				continue
			}
		}
		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		plain := pkt
		if l.cfg.Cipher != nil {
			dec, err := l.cfg.Cipher.Decrypt(pkt)
			if err != nil {
				continue
			}
			plain = dec
		}
		if len(plain) < int(IKCP_OVERHEAD) {
			continue
		}
		conv := binary.LittleEndian.Uint32(plain[0:4])
		key := fmt.Sprintf("%s#%d", raddr.String(), conv)

		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		conn, exists := l.sessions[key]
		if !exists {
			now := time.Now()
			l.sweepTombstonesLocked(now)
			if until, dead := l.tombstones[key]; dead && now.Before(until) {
				l.mu.Unlock()
				continue
			}
			if !isInitialKCPSegment(plain, conv) {
				// Only a connection's first PUSH may create a session; stray
				// packets (e.g. for a pre-restart connection) are dropped.
				l.mu.Unlock()
				continue
			}
			connCfg := l.cfg
			connCfg.Cipher = l.cfg.Cipher
			var created *KCPConn
			created = newKCPConn(conv, l.udpConn, raddr, false, connCfg, func() {
				l.mu.Lock()
				if l.sessions[key] == created {
					delete(l.sessions, key)
				}
				if !l.closed {
					l.tombstones[key] = time.Now().Add(kcpTombstoneTTL)
				}
				l.mu.Unlock()
			})
			conn = created
			l.sessions[key] = conn
			l.mu.Unlock()

			// Feed first packet before queuing so KCP state initializes.
			if err := conn.inputPlain(plain); err != nil {
				_ = conn.Close()
				continue
			}

			select {
			case l.acceptCh <- conn:
			case <-l.done:
				_ = conn.Close()
				return
			default:
				// Accept backlog full: never stall the shared read loop.
				_ = conn.Close()
			}
			continue
		}
		l.mu.Unlock()

		_ = conn.inputPlain(plain)
	}
}

// isInitialKCPSegment reports whether a decrypted datagram carries the first
// data segment of a new connection (PUSH with sn == 0 for conv).
func isInitialKCPSegment(data []byte, conv uint32) bool {
	offset := 0
	for len(data)-offset >= int(IKCP_OVERHEAD) {
		if binary.LittleEndian.Uint32(data[offset:offset+4]) != conv {
			return false
		}
		cmd := data[offset+4]
		sn := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		length := int(binary.LittleEndian.Uint32(data[offset+20 : offset+24]))
		offset += int(IKCP_OVERHEAD)
		if length < 0 || len(data)-offset < length {
			return false
		}
		if cmd == IKCP_CMD_PUSH && sn == 0 {
			return true
		}
		offset += length
	}
	return false
}

func (l *KCPListener) sweepTombstonesLocked(now time.Time) {
	if len(l.tombstones) == 0 || now.Sub(l.lastSweep) < time.Second {
		return
	}
	l.lastSweep = now
	for key, until := range l.tombstones {
		if !now.Before(until) {
			delete(l.tombstones, key)
		}
	}
}

func (l *KCPListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.acceptCh:
		return conn, nil
	case <-l.done:
		return nil, ErrListenerClosed
	}
}

func (l *KCPListener) Addr() net.Addr {
	return l.udpConn.LocalAddr()
}

func (l *KCPListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	conns := make([]*KCPConn, 0, len(l.sessions))
	for _, c := range l.sessions {
		conns = append(conns, c)
	}
	l.mu.Unlock()

	close(l.done)
	for _, c := range conns {
		_ = c.Close()
	}
	return l.udpConn.Close()
}

// DialKCP opens a KCPConn to the remote UDP address.
func DialKCP(addr string, cfg KCPConfig) (*KCPConn, error) {
	return DialKCPWithNetwork(addr, cfg, DefaultNetworkConfig())
}

func DialKCPWithNetwork(addr string, cfg KCPConfig, network NetworkConfig) (*KCPConn, error) {
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	var localAddr *net.UDPAddr
	if network.Mode == NetworkModeSingle && network.InterfaceName != "" {
		iface, err := net.InterfaceByName(network.InterfaceName)
		if err != nil {
			return nil, err
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
				localAddr = &net.UDPAddr{IP: ipNet.IP.To4(), Port: 0}
				break
			}
		}
		if localAddr == nil {
			return nil, fmt.Errorf("transport: interface %q has no IPv4 address", network.InterfaceName)
		}
	}
	pc, err := net.ListenUDP("udp4", localAddr)
	if err != nil {
		return nil, err
	}
	conv := cfg.Conv
	if conv == 0 {
		var b [4]byte
		_, _ = rand.Read(b[:])
		conv = binary.LittleEndian.Uint32(b[:])
		if conv == 0 {
			conv = 1
		}
	}
	kc := newKCPConn(conv, pc, raddr, true, cfg, nil)
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				select {
				case <-kc.done:
					return
				default:
					continue
				}
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			_ = kc.input(pkt)
		}
	}()
	return kc, nil
}

type IncomingStream struct {
	Kind   protocol.StreamKind
	Stream io.ReadWriteCloser
}

type videoMediaItem struct {
	pkt      []byte
	keyFrame bool
}

var sessionSeq atomic.Uint64

// SmuxSession wraps a KCPConn (or any net.Conn) with github.com/xtaci/smux and implements core.Session.
type SmuxSession struct {
	id         string
	conn       net.Conn
	mux        *smux.Session
	onEnvelope func(core.Session, protocol.Envelope)
	onStream   func(core.Session, protocol.StreamKind, io.ReadWriteCloser)

	closeMu      sync.Mutex
	onClose      func(core.Session)
	onCloseFired bool

	ctrlMu      sync.Mutex
	ctrlStream  *smux.Stream
	mediaStream *smux.Stream

	videoMediaCh chan videoMediaItem
	audioMediaCh chan []byte

	incomingCh chan IncomingStream
	done       chan struct{}
	closeOnce  sync.Once
}

func NewSmuxSession(conn net.Conn, isServer bool, onEnvelope func(core.Session, protocol.Envelope)) (*SmuxSession, error) {
	smuxCfg := smux.DefaultConfig()
	smuxCfg.KeepAliveInterval = 3 * time.Second
	smuxCfg.KeepAliveTimeout = 12 * time.Second
	smuxCfg.MaxReceiveBuffer = 8 * 1024 * 1024
	smuxCfg.MaxStreamBuffer = 2 * 1024 * 1024

	var mux *smux.Session
	var err error
	if isServer {
		mux, err = smux.Server(conn, smuxCfg)
	} else {
		mux, err = smux.Client(conn, smuxCfg)
	}
	if err != nil {
		return nil, err
	}

	s := &SmuxSession{
		id:           fmt.Sprintf("sess-%d", sessionSeq.Add(1)),
		conn:         conn,
		mux:          mux,
		onEnvelope:   onEnvelope,
		videoMediaCh: make(chan videoMediaItem, mediaQueueCapacity),
		audioMediaCh: make(chan []byte, audioQueueCapacity),
		incomingCh:   make(chan IncomingStream, 64),
		done:         make(chan struct{}),
	}
	go s.acceptLoop()
	go s.mediaWriteLoop()
	return s, nil
}

func (s *SmuxSession) SetID(id string) {
	s.id = id
}

func (s *SmuxSession) SetOnEnvelope(fn func(core.Session, protocol.Envelope)) {
	s.onEnvelope = fn
}

func (s *SmuxSession) SetOnStream(fn func(core.Session, protocol.StreamKind, io.ReadWriteCloser)) {
	s.onStream = fn
}

// SetOnClose registers fn to run once when the session closes. If the session
// already closed (for example its background loops failed before the caller
// got a chance to register), fn is invoked immediately.
func (s *SmuxSession) SetOnClose(fn func(core.Session)) {
	s.closeMu.Lock()
	if s.onCloseFired {
		s.closeMu.Unlock()
		if fn != nil {
			fn(s)
		}
		return
	}
	s.onClose = fn
	s.closeMu.Unlock()
}

func (s *SmuxSession) isClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *SmuxSession) ID() string {
	return s.id
}

func (s *SmuxSession) RemoteAddr() string {
	if s.conn == nil || s.conn.RemoteAddr() == nil {
		return ""
	}
	return s.conn.RemoteAddr().String()
}

func (s *SmuxSession) OpenStream(kind protocol.StreamKind) (io.ReadWriteCloser, error) {
	stream, err := s.mux.OpenStream()
	if err != nil {
		return nil, err
	}
	if _, err := stream.Write([]byte{byte(kind)}); err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

func (s *SmuxSession) AcceptStream() (protocol.StreamKind, io.ReadWriteCloser, error) {
	select {
	case item := <-s.incomingCh:
		return item.Kind, item.Stream, nil
	case <-s.done:
		return 0, nil, io.EOF
	}
}

func (s *SmuxSession) SendEnvelope(env protocol.Envelope) error {
	raw, err := protocol.EncodeEnvelope(env)
	if err != nil {
		return err
	}
	if isMediaEnvelope(env) {
		return s.enqueueMediaFrame(env.Type, raw, isVideoKeyFrame(env))
	}
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()

	select {
	case <-s.done:
		return ErrConnClosed
	default:
	}

	if s.ctrlStream == nil {
		st, err := s.mux.OpenStream()
		if err != nil {
			return err
		}
		_ = st.SetWriteDeadline(time.Now().Add(ctrlWriteTimeout))
		if _, err := st.Write([]byte{streamTagEnvelope}); err != nil {
			_ = st.Close()
			return err
		}
		s.ctrlStream = st
	}
	_ = s.ctrlStream.SetWriteDeadline(time.Now().Add(ctrlWriteTimeout))
	if err := protocol.WriteFrame(s.ctrlStream, protocol.StreamControl, env.Type, raw); err != nil {
		_ = s.ctrlStream.Close()
		s.ctrlStream = nil
		return err
	}
	_ = s.ctrlStream.SetWriteDeadline(time.Time{})
	return nil
}

func isMediaEnvelope(env protocol.Envelope) bool {
	return env.Type == protocol.MsgVideoFrame || env.Type == protocol.MsgAudioFrame
}

func isVideoKeyFrame(env protocol.Envelope) bool {
	if env.Type != protocol.MsgVideoFrame || len(env.Payload) == 0 {
		return false
	}
	var pkt struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(env.Payload, &pkt); err != nil {
		return false
	}
	return len(pkt.Data) >= 7 && pkt.Data[0] == 'N' && pkt.Data[1] == 'X' && pkt.Data[2] == 'V' && pkt.Data[3] == 'F' && (pkt.Data[6]&0x01) != 0
}

func (s *SmuxSession) enqueueMediaFrame(msgType protocol.MsgType, raw []byte, keyFrame bool) error {
	select {
	case <-s.done:
		return ErrConnClosed
	default:
	}
	var frameBuf bytes.Buffer
	if err := protocol.WriteFrame(&frameBuf, protocol.StreamMedia, msgType, raw); err != nil {
		return err
	}
	pkt := frameBuf.Bytes()
	if msgType == protocol.MsgAudioFrame {
		select {
		case s.audioMediaCh <- pkt:
		default:
			select {
			case <-s.audioMediaCh:
			default:
			}
			select {
			case s.audioMediaCh <- pkt:
			default:
			}
		}
		return nil
	}
	item := videoMediaItem{pkt: pkt, keyFrame: keyFrame}
	if keyFrame {
		// A fresh keyframe supersedes all older queued frames.
		for {
			select {
			case <-s.videoMediaCh:
			default:
				goto enqueueKeyframe
			}
		}
	enqueueKeyframe:
		select {
		case s.videoMediaCh <- item:
		default:
		}
		return nil
	}
	select {
	case s.videoMediaCh <- item:
	default:
	}
	return nil
}

func (s *SmuxSession) mediaWriteLoop() {
	defer func() {
		if s.mediaStream != nil {
			_ = s.mediaStream.Close()
			s.mediaStream = nil
		}
	}()
	writePacket := func(pkt []byte) bool {
		if len(pkt) == 0 {
			return true
		}
		if s.mediaStream == nil {
			st, err := s.mux.OpenStream()
			if err != nil {
				return false
			}
			_ = st.SetWriteDeadline(time.Now().Add(mediaWriteTimeout))
			if _, err := st.Write([]byte{streamTagMediaEnv}); err != nil {
				_ = st.Close()
				return false
			}
			s.mediaStream = st
		}
		_ = s.mediaStream.SetWriteDeadline(time.Now().Add(mediaWriteTimeout))
		if _, err := s.mediaStream.Write(pkt); err != nil {
			_ = s.mediaStream.Close()
			s.mediaStream = nil
			return false
		}
		_ = s.mediaStream.SetWriteDeadline(time.Time{})
		return true
	}
	for {
		select {
		case <-s.done:
			return
		case pkt := <-s.audioMediaCh:
			writePacket(pkt)
		case item := <-s.videoMediaCh:
			if !writePacket(item.pkt) && item.keyFrame && !s.mux.IsClosed() {
				writePacket(item.pkt)
			}
		}
	}
}

func (s *SmuxSession) acceptLoop() {
	defer func() {
		_ = s.Close()
	}()
	for {
		stream, err := s.mux.AcceptStream()
		if err != nil {
			return
		}
		go s.handleAcceptedStream(stream)
	}
}

func (s *SmuxSession) handleAcceptedStream(stream *smux.Stream) {
	var tag [1]byte
	if _, err := io.ReadFull(stream, tag[:]); err != nil {
		_ = stream.Close()
		return
	}
	if tag[0] == streamTagEnvelope || tag[0] == streamTagMediaEnv {
		defer stream.Close()
		for {
			_, _, payload, err := protocol.ReadFrame(stream)
			if err != nil {
				return
			}
			env, err := protocol.DecodeEnvelope(payload)
			if err != nil {
				continue
			}
			if s.onEnvelope != nil {
				s.onEnvelope(s, env)
			}
		}
	}

	kind := protocol.StreamKind(tag[0])
	if s.onStream != nil {
		s.onStream(s, kind, stream)
		return
	}
	select {
	case s.incomingCh <- IncomingStream{Kind: kind, Stream: stream}:
	case <-s.done:
		_ = stream.Close()
	}
}

func (s *SmuxSession) KCPStats() (KCPStats, bool) {
	if kc, ok := s.conn.(interface{ Stats() KCPStats }); ok {
		return kc.Stats(), true
	}
	return KCPStats{}, false
}

func (s *SmuxSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		s.ctrlMu.Lock()
		if s.ctrlStream != nil {
			_ = s.ctrlStream.Close()
			s.ctrlStream = nil
		}
		s.ctrlMu.Unlock()
		if s.mux != nil {
			err = s.mux.Close()
		}
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.closeMu.Lock()
		s.onCloseFired = true
		onClose := s.onClose
		s.onClose = nil
		s.closeMu.Unlock()
		if onClose != nil {
			onClose(s)
		}
	})
	return err
}
