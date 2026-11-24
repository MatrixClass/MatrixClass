package transport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"nexlab/internal/core"
	"nexlab/internal/protocol"

	"golang.org/x/time/rate"
)

const (
	defaultRingBufferSize  = 64
	defaultMcastChunkSize  = 1200
	mcastPacketMagic       = uint16(0x4E58) // "NX"
	mcastHeaderSize        = 14             // magic(2) + seq(8) + idx(2) + total(2)
	internalNackRequestID  = "__nexlab_mcast_nack__"
	internalFallbackReqID  = "__nexlab_mcast_fallback__"
	internalMcastInfoReqID = "__nexlab_mcast_info__"
	internalMcastAckReqID  = "__nexlab_mcast_ack__"

	// maxMcastChunks bounds the number of chunks a single multicast frame may
	// claim (about 9.8 MB at defaultMcastChunkSize) so a hostile or corrupt
	// header cannot force huge reassembly allocations.
	maxMcastChunks = 8192
	// mcastResyncFactor * ringSize is how far a sequence may jump backwards
	// before the receiver assumes the sender restarted and resynchronizes.
	mcastResyncFactor = 4
)

// listenUDPFunc is swapped in tests to exercise StartTeacher error paths.
var listenUDPFunc = net.ListenUDP

var (
	ErrNoSession       = errors.New("transport: session not found")
	ErrNotStarted      = errors.New("transport: manager not started")
	ErrAlreadyStarted  = errors.New("transport: manager already started")
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

func WithNetworkConfig(cfg NetworkConfig) ManagerOption {
	return func(m *Manager) {
		m.networkConfig = cfg.normalized()
		m.discoveryCfg.Network = m.networkConfig
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
		if err != nil {
			log.Printf("transport: invalid cipher key: %v", err)
			m.optErr = fmt.Errorf("transport: cipher key: %w", err)
			return
		}
		m.cipher = c
		m.kcpCfg.Cipher = c
	}
}

func WithMulticastRateLimit(bytesPerSec int) ManagerOption {
	return func(m *Manager) {
		if bytesPerSec > 0 {
			burst := bytesPerSec
			if burst < 64*1024 {
				burst = 64 * 1024
			}
			m.mcastLimiter = rate.NewLimiter(rate.Limit(bytesPerSec), burst)
		}
	}
}

// WithTeacherIdentity makes a teacher sign discovery beacons and require the
// authenticated, encrypted handshake on every student connection.
func WithTeacherIdentity(id *TeacherIdentity) ManagerOption {
	return func(m *Manager) {
		m.identity = id
	}
}

// WithTeacherTrust makes a student accept only signed beacons and teachers
// whose identity matches trust, and encrypts the whole session.
func WithTeacherTrust(trust TeacherTrust) ManagerOption {
	return func(m *Manager) {
		m.trust = trust
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

type mcastInfoPayload struct {
	MulticastIP string `json:"multicast_ip"`
	McastPort   int    `json:"mcast_port"`
	Key         []byte `json:"key,omitempty"`
}

type fallbackTask struct {
	targets []*SmuxSession
	seq     uint64
	data    []byte
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
	startMu             sync.Mutex
	mcastStartMu        sync.Mutex
	role                core.Role
	kcpCfg              KCPConfig
	discoveryCfg        DiscoveryConfig
	explicitTeacherAddr string
	explicitMcastListen string
	cipher              *FrameCipher
	identity            *TeacherIdentity
	trust               TeacherTrust
	mcastKey            []byte
	mcastCipher         atomic.Pointer[FrameCipher]
	fecDataShards       int
	fecParityShards     int
	fecEncoder          *FECEncoder
	fecDecoder          *FECDecoder
	mcastLimiter        *rate.Limiter
	optErr              error

	listener    *KCPListener
	broadcaster *BeaconBroadcaster
	beacon      protocol.DiscoveryBeacon
	mcastConn   *net.UDPConn
	mcastTarget *net.UDPAddr
	mcastRxConn net.PacketConn
	mcastAddr   string
	// mcastSourceIP is the teacher IP multicast packets must originate from
	// (student role). nil disables filtering (unknown or loopback teacher).
	mcastSourceIP net.IP

	sessions         map[string]*SmuxSession
	aliases          map[string]*SmuxSession
	mcastConfirmed   map[*SmuxSession]bool
	multicastEnabled atomic.Bool
	mcastAckSent     atomic.Bool
	reconnecting     atomic.Bool
	networkWatcher   atomic.Bool
	nackWatcher      atomic.Bool
	studentChannel   string
	networkConfig    NetworkConfig
	connectionGen    atomic.Uint64

	networkMu          sync.RWMutex
	activeInterfaces   []string
	interfaceDetails   []NetworkInterfaceStatus
	localAddresses     []string
	localAddress       string
	connectionAddress  string
	lastConnectedAt    time.Time
	reconnects         uint64
	lastReconnectCause string

	ringSize int
	ring     []ringEntry
	mcastSeq atomic.Uint64

	onSessionCb         func(core.Session)
	onConnectionStateCb func(connected bool, reason string)
	onEnvelopeCb        func(core.Session, protocol.Envelope)
	onMulticastCb       func([]byte)

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
	fallbackCh       chan fallbackTask
	mcastPktPool     sync.Pool
	mcastTxBuf       []byte
	mcastTxMu        sync.Mutex

	statsMu       sync.Mutex
	lastStatsTime time.Time
	lastTxBytes   uint64
	lastRxBytes   uint64
	lastTxRate    uint64
	lastRxRate    uint64

	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	started  bool
	starting bool
}

// NetworkStatus describes the transport's current interface and reconnect state.
type NetworkStatus struct {
	Interfaces          []string
	InterfaceDetails    []NetworkInterfaceStatus
	LocalAddresses      []string
	LocalAddress        string
	ConnectionAddress   string
	LastConnectedAt     time.Time
	Reconnects          uint64
	LastReconnectReason string
}

type NetworkInterfaceStatus struct {
	Index     int
	Name      string
	Addresses []string
}

func NewManager(opts ...ManagerOption) *Manager {
	m := &Manager{
		kcpCfg:         DefaultKCPConfig(),
		discoveryCfg:   DefaultDiscoveryConfig(),
		networkConfig:  DefaultNetworkConfig(),
		sessions:       make(map[string]*SmuxSession),
		aliases:        make(map[string]*SmuxSession),
		mcastConfirmed: make(map[*SmuxSession]bool),
		ringSize:       defaultRingBufferSize,
		mcastReasm:     make(map[uint64]*mcastReassembly),
		mcastDelivered: make(map[uint64]struct{}),
		nackPending:    make(map[uint64]time.Time),
		fallbackCh:     make(chan fallbackTask, 32),
		mcastPktPool: sync.Pool{
			New: func() any {
				return make([]byte, 64*1024)
			},
		},
		mcastTxBuf:    make([]byte, mcastHeaderSize+defaultMcastChunkSize),
		lastStatsTime: time.Now(),
	}
	m.multicastEnabled.Store(true)
	for _, opt := range opts {
		opt(m)
	}
	m.discoveryCfg.Network = m.networkConfig
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

// OnConnectionState reports student transport connectivity changes. The
// callback is informational and is never invoked for teacher managers.
func (m *Manager) OnConnectionState(fn func(connected bool, reason string)) {
	m.mu.Lock()
	m.onConnectionStateCb = fn
	m.mu.Unlock()
}

func (m *Manager) notifyConnectionState(connected bool, reason string) {
	m.mu.RLock()
	cb := m.onConnectionStateCb
	role := m.role
	m.mu.RUnlock()
	if cb != nil && role == core.RoleStudent {
		cb(connected, reason)
	}
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

func (m *Manager) SetMulticastEnabled(enabled bool) {
	m.multicastEnabled.Store(enabled)
}

func (m *Manager) MulticastEnabled() bool {
	return m.multicastEnabled.Load()
}

func (m *Manager) recordReconnect(reason string) {
	m.networkMu.Lock()
	m.reconnects++
	m.lastReconnectCause = reason
	m.networkMu.Unlock()
}

func (m *Manager) recordConnection(address, localAddress string, interfaces []string, localAddresses []string) {
	m.networkMu.Lock()
	m.connectionAddress = address
	m.localAddresses = append([]string(nil), localAddresses...)
	m.localAddress = localAddress
	m.lastConnectedAt = time.Now()
	m.activeInterfaces = append([]string(nil), interfaces...)
	m.interfaceDetails = snapshotInterfaceDetails()
	m.networkMu.Unlock()
}

func (m *Manager) nextConnectionGeneration() uint64 {
	return m.connectionGen.Add(1)
}

func (m *Manager) invalidateConnectionGeneration() {
	m.connectionGen.Add(1)
}

func (m *Manager) connectionGenerationCurrent(generation uint64) bool {
	return generation != 0 && m.connectionGen.Load() == generation
}

func (m *Manager) NetworkStatus() NetworkStatus {
	m.networkMu.RLock()
	defer m.networkMu.RUnlock()
	return NetworkStatus{
		Interfaces:          append([]string(nil), m.activeInterfaces...),
		InterfaceDetails:    cloneInterfaceDetails(m.interfaceDetails),
		LocalAddresses:      append([]string(nil), m.localAddresses...),
		LocalAddress:        m.localAddress,
		ConnectionAddress:   m.connectionAddress,
		LastConnectedAt:     m.lastConnectedAt,
		Reconnects:          m.reconnects,
		LastReconnectReason: m.lastReconnectCause,
	}
}

func (m *Manager) NetworkConfig() NetworkConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.networkConfig
}

func (m *Manager) SetNetworkConfig(cfg NetworkConfig) {
	cfg = cfg.normalized()
	m.mu.Lock()
	m.networkConfig = cfg
	m.discoveryCfg.Network = cfg
	m.mu.Unlock()
}

// StartTeacher starts KCP listener, discovery beacon broadcaster, and optional UDP multicast sender.
func (m *Manager) StartTeacher(ctx context.Context, listenAddr string, beacon protocol.DiscoveryBeacon) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return ErrConnClosed
	}
	if m.optErr != nil {
		return m.optErr
	}
	if m.started || m.starting {
		return ErrAlreadyStarted
	}
	m.starting = true
	m.role = core.RoleTeacher
	m.ctx, m.cancel = context.WithCancel(ctx)

	var listener *KCPListener
	var mcastConn *net.UDPConn
	fail := func(err error) error {
		if listener != nil {
			_ = listener.Close()
		}
		if mcastConn != nil {
			_ = mcastConn.Close()
		}
		if m.cancel != nil {
			m.cancel()
		}
		m.ctx, m.cancel = nil, nil
		m.listener = nil
		m.mcastConn = nil
		m.mcastTarget = nil
		m.broadcaster = nil
		m.starting = false
		return err
	}

	listener, err := ListenKCP(listenAddr, m.kcpCfg)
	if err != nil {
		listener = nil
		return fail(err)
	}

	if udpAddr, ok := listener.Addr().(*net.UDPAddr); ok && beacon.KCPPort == 0 {
		beacon.KCPPort = udpAddr.Port
	}
	if beacon.Role == "" {
		beacon.Role = string(core.RoleTeacher)
	}
	if m.identity != nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fail(err)
		}
		mc, err := NewFrameCipher(key)
		if err != nil {
			return fail(err)
		}
		m.mcastKey = key
		m.mcastCipher.Store(mc)
		beacon = SignBeacon(m.identity, beacon)
	}

	var mcastTarget *net.UDPAddr
	if beacon.MulticastIP != "" && beacon.McastPort > 0 {
		target, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(beacon.MulticastIP, strconv.Itoa(beacon.McastPort)))
		if err != nil {
			return fail(err)
		}
		conn, err := listenUDPFunc("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			return fail(err)
		}
		mcastConn = conn
		_ = mcastConn.SetWriteBuffer(2 * 1024 * 1024)
		mcastTarget = target
	}

	var bc *BeaconBroadcaster
	if m.discoveryCfg.Port > 0 {
		bc, err = StartBeaconBroadcaster(m.ctx, beacon, m.discoveryCfg)
		if err != nil {
			return fail(err)
		}
	}

	m.listener = listener
	m.beacon = beacon
	m.mcastTarget = mcastTarget
	m.mcastConn = mcastConn
	m.broadcaster = bc

	go m.acceptLoop()
	m.started = true
	m.starting = false
	go m.fallbackLoop()
	return nil
}

func (m *Manager) fallbackLoop() {
	m.mu.RLock()
	ctx := m.ctx
	m.mu.RUnlock()
	if ctx == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case <-ctx.Done():
			return
		case task, ok := <-m.fallbackCh:
			if !ok {
				return
			}
			if ctx.Err() != nil {
				return
			}
			if len(task.targets) > 0 {
				_ = m.sendFallbackToSessions(task.targets, task.seq, task.data)
			}
		}
	}
}

func (m *Manager) acceptLoop() {
	for {
		conn, err := m.listener.Accept()
		if err != nil {
			return
		}
		if m.identity == nil {
			m.serveAccepted(conn)
			continue
		}
		// The handshake waits for the peer, so it must not stall Accept.
		go func(raw net.Conn) {
			secured, err := ServerHandshake(raw, m.identity)
			if err != nil {
				log.Printf("transport: rejected %s: %v", raw.RemoteAddr(), err)
				_ = raw.Close()
				return
			}
			m.serveAccepted(secured)
		}(conn)
	}
}

func (m *Manager) serveAccepted(conn net.Conn) {
	sess, err := NewSmuxSession(conn, true, m.handleEnvelope)
	if err != nil {
		_ = conn.Close()
		return
	}
	sess.SetOnClose(func(closed core.Session) {
		m.removeSession(closed.ID())
	})
	if !m.addSessionIfCurrent(sess, 0) {
		_ = sess.Close()
		return
	}
	m.mu.RLock()
	mcastIP := m.beacon.MulticastIP
	mcastPort := m.beacon.McastPort
	mcastKey := m.mcastKey
	cb := m.onSessionCb
	m.mu.RUnlock()
	if mcastIP != "" && mcastPort > 0 {
		if raw, err := json.Marshal(mcastInfoPayload{MulticastIP: mcastIP, McastPort: mcastPort, Key: mcastKey}); err == nil {
			_ = sess.SendEnvelope(protocol.Envelope{
				Type:      protocol.MsgHelloAck,
				RequestID: internalMcastInfoReqID,
				Timestamp: time.Now().UnixMilli(),
				Payload:   raw,
			})
		}
	}
	if cb != nil {
		cb(sess)
	}
}

// StartStudent discovers the teacher on channel (or uses explicit addr), connects via KCP+smux, and listens for multicast.
func (m *Manager) StartStudent(ctx context.Context, channel string) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrConnClosed
	}
	if m.optErr != nil {
		m.mu.Unlock()
		return m.optErr
	}
	if m.started || m.starting {
		m.mu.Unlock()
		return ErrAlreadyStarted
	}
	m.starting = true
	m.nextConnectionGeneration()
	m.role = core.RoleStudent
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.studentChannel = channel
	m.mu.Unlock()

	if err := m.connectStudentOnce(channel); err != nil {
		m.notifyConnectionState(false, err.Error())
		m.mu.Lock()
		if !m.closed {
			if m.cancel != nil {
				m.cancel()
			}
			m.cancel = nil
			m.ctx = nil
		}
		m.starting = false
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	m.started = true
	m.starting = false
	studentCtx := m.ctx
	studentGeneration := m.connectionGen.Load()
	m.mu.Unlock()
	go m.studentConnectionSupervisor(studentCtx, studentGeneration)
	return nil
}

// ReconnectStudent replaces the current student session and starts discovery
// again on the supplied channel without discarding the manager configuration.
func (m *Manager) ReconnectStudent(ctx context.Context, channel string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrConnClosed
	}
	if m.role != core.RoleStudent || !m.started {
		m.mu.Unlock()
		return m.StartStudent(ctx, channel)
	}
	m.started = false
	m.starting = false
	oldCancel := m.cancel
	m.cancel = nil
	sessions := make([]*SmuxSession, 0, len(m.sessions))
	for _, sess := range m.sessions {
		sessions = append(sessions, sess)
	}
	m.sessions = make(map[string]*SmuxSession)
	m.aliases = make(map[string]*SmuxSession)
	m.studentChannel = channel
	m.invalidateConnectionGeneration()
	m.mu.Unlock()
	if oldCancel != nil {
		oldCancel()
	}
	for _, sess := range sessions {
		_ = sess.Close()
	}
	return m.StartStudent(ctx, channel)
}

// studentConnectionSupervisor keeps the reconnect path alive even when a
// network transition removes a session without immediately running its close
// callback. It exits only when the manager is closed or its start context is
// cancelled; a healthy session makes it a cheap periodic check.
func (m *Manager) studentConnectionSupervisor(ctx context.Context, generation uint64) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !m.connectionGenerationCurrent(generation) {
			return
		}
		m.mu.RLock()
		closed := m.closed
		role := m.role
		hasSession := len(m.sessions) > 0
		m.mu.RUnlock()
		if closed || role != core.RoleStudent {
			return
		}
		if !hasSession {
			m.scheduleStudentReconnect(generation)
		}
	}
}

func (m *Manager) connectStudentOnce(channel string) error {
	m.mu.RLock()
	if m.closed || m.ctx == nil || m.ctx.Err() != nil {
		m.mu.RUnlock()
		return ErrConnClosed
	}
	ctx := m.ctx
	connectionGeneration := m.connectionGen.Load()
	teacherAddr := m.explicitTeacherAddr
	dPort := m.discoveryCfg.Port
	explicitMcast := m.explicitMcastListen
	networkConfig := m.networkConfig
	trust := m.trust
	m.mu.RUnlock()

	var discovered DiscoveredTeacher
	if teacherAddr == "" {
		listenAddr := discoveryListenAddr(dPort, networkConfig)
		if dPort <= 0 {
			listenAddr = fmt.Sprintf(":%d", DefaultDiscoveryPort)
		}
		d, err := ListenDiscoveryFiltered(ctx, listenAddr, channel, trustedBeaconFilter(trust))
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

	kcpConn, err := DialKCPWithNetwork(teacherAddr, m.kcpCfg, networkConfig)
	if err != nil {
		return err
	}
	var sessConn net.Conn = kcpConn
	if trust != nil {
		secured, _, err := ClientHandshake(kcpConn, trust)
		if err != nil {
			_ = kcpConn.Close()
			return err
		}
		sessConn = secured
	}
	sess, err := NewSmuxSession(sessConn, false, m.handleEnvelope)
	if err != nil {
		_ = kcpConn.Close()
		return err
	}
	sess.SetOnClose(func(closed core.Session) {
		m.removeSession(closed.ID())
		m.notifyConnectionState(false, "教师端连接已断开")
		m.scheduleStudentReconnect(connectionGeneration)
	})
	if !m.addSessionIfCurrent(sess, connectionGeneration) {
		_ = sess.Close()
		return ErrConnClosed
	}
	m.resetMulticastRxState(teacherSourceIP(kcpConn.RemoteAddr()))
	localAddress := ""
	if addr := kcpConn.LocalAddr(); addr != nil {
		localAddress = addr.String()
	}
	m.recordConnection(teacherAddr, localAddress, networkInterfaceSnapshot(), localAddressSnapshot())

	mcastAddrStr := explicitMcast
	if mcastAddrStr == "" && discovered.Beacon.MulticastIP != "" && discovered.Beacon.McastPort > 0 {
		mcastAddrStr = net.JoinHostPort(discovered.Beacon.MulticastIP, strconv.Itoa(discovered.Beacon.McastPort))
	}
	if mcastAddrStr != "" {
		if err := m.startMulticastReceiverWithNetwork(mcastAddrStr, networkConfig); err != nil {
			_ = err
		}
	}

	m.mu.RLock()
	cb := m.onSessionCb
	m.mu.RUnlock()
	if cb != nil {
		cb(sess)
	}
	m.notifyConnectionState(true, "")
	return nil
}

// trustedBeaconFilter skips unsigned beacons and, once a teacher is pinned,
// beacons signed by any other key. A nil trust accepts every beacon.
func trustedBeaconFilter(trust TeacherTrust) func(protocol.DiscoveryBeacon) bool {
	if trust == nil {
		return nil
	}
	return func(b protocol.DiscoveryBeacon) bool {
		pub, err := VerifyBeacon(b)
		if err != nil {
			return false
		}
		pinned := trust.Pinned()
		return pinned == "" || pinned == Fingerprint(pub)
	}
}

// secured reports whether this manager runs the authenticated protocol.
func (m *Manager) secured() bool {
	return m.identity != nil || m.trust != nil
}

func (m *Manager) scheduleStudentReconnect(closedGeneration uint64) {
	m.mu.RLock()
	isStudent := m.role == core.RoleStudent
	closed := m.closed
	ctx := m.ctx
	channel := m.studentChannel
	generation := m.connectionGen.Load()
	m.mu.RUnlock()
	if closedGeneration != 0 && closedGeneration != generation {
		return
	}
	if !isStudent || closed || ctx == nil || ctx.Err() != nil {
		return
	}
	if !m.reconnecting.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer m.reconnecting.Store(false)
		backoff := 500 * time.Millisecond
		const maxBackoff = 5 * time.Second
		for {
			if !m.connectionGenerationCurrent(generation) {
				return
			}
			m.mu.RLock()
			stillOpen := !m.closed && m.ctx != nil && m.ctx.Err() == nil
			m.mu.RUnlock()
			if !stillOpen {
				return
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if !m.connectionGenerationCurrent(generation) {
				return
			}
			if err := m.connectStudentOnce(channel); err == nil {
				return
			}
			m.recordReconnect("KCP reconnect attempt failed")
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}()
}

func networkInterfaceSnapshot() []string {
	ifaces, err := interfaceEnumerator()
	if err != nil {
		return nil
	}
	usable := usableInterfaces(ifaces)
	snapshot := make([]string, 0, len(usable))
	for _, iface := range usable {
		snapshot = append(snapshot, fmt.Sprintf("%d:%s", iface.Index, iface.Name))
	}
	sort.Strings(snapshot)
	return snapshot
}

func localAddressSnapshot() []string {
	ifaces, err := interfaceEnumerator()
	if err != nil {
		return nil
	}
	var addresses []string
	for _, iface := range usableInterfaces(ifaces) {
		ifAddrs, err := interfaceAddressEnumerator(iface)
		if err != nil {
			continue
		}
		for _, addr := range ifAddrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP != nil {
				ip := ipNet.IP
				addresses = append(addresses, ip.String())
			}
		}
	}
	sort.Strings(addresses)
	return addresses
}

func snapshotInterfaceDetails() []NetworkInterfaceStatus {
	ifaces, err := interfaceEnumerator()
	if err != nil {
		return nil
	}
	usable := usableInterfaces(ifaces)
	details := make([]NetworkInterfaceStatus, 0, len(usable))
	for _, iface := range usable {
		detail := NetworkInterfaceStatus{Index: iface.Index, Name: iface.Name}
		ifAddrs, err := interfaceAddressEnumerator(iface)
		if err == nil {
			for _, addr := range ifAddrs {
				if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP != nil {
					detail.Addresses = append(detail.Addresses, ipNet.IP.String())
				}
			}
		}
		details = append(details, detail)
	}
	return details
}

func cloneInterfaceDetails(details []NetworkInterfaceStatus) []NetworkInterfaceStatus {
	cloned := make([]NetworkInterfaceStatus, len(details))
	for i, detail := range details {
		cloned[i] = detail
		cloned[i].Addresses = append([]string(nil), detail.Addresses...)
	}
	return cloned
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (m *Manager) networkWatchLoop() {
	defer m.networkWatcher.Store(false)
	last := networkInterfaceSnapshot()
	m.networkMu.Lock()
	m.activeInterfaces = append([]string(nil), last...)
	m.networkMu.Unlock()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			current := networkInterfaceSnapshot()
			if sameStrings(last, current) {
				continue
			}
			last = current
			m.networkMu.Lock()
			m.activeInterfaces = append([]string(nil), current...)
			m.networkMu.Unlock()
			m.recordReconnect("network interface changed")
			m.restartMulticastReceiver()
		}
	}
}

type multiPacket struct {
	data []byte
	addr net.Addr
}

func (m *Manager) addSession(s *SmuxSession) {
	m.addSessionIfCurrent(s, 0)
}

// addSessionIfCurrent registers s unless the manager is closed, s is already
// closed, or a non-zero generation is stale. It reports whether s was added.
func (m *Manager) addSessionIfCurrent(s *SmuxSession, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || s.isClosed() {
		return false
	}
	if generation != 0 && m.connectionGen.Load() != generation {
		return false
	}
	m.sessions[s.ID()] = s
	return true
}

func (m *Manager) removeSession(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	if s != nil {
		delete(m.mcastConfirmed, s)
	}
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
			if existing, ok := m.aliases[env.SenderID]; !ok || existing == sm || existing.isClosed() || sameRemoteHost(existing, sm) {
				m.aliases[env.SenderID] = sm
			}
			m.mu.Unlock()
		}
	}

	if env.Type == protocol.MsgMediaNACK && env.RequestID == internalNackRequestID {
		var req mediaNACKPayload
		if err := json.Unmarshal(env.Payload, &req); err == nil {
			if len(req.Seqs) > 0 {
				go m.serveNACK(sess, req.Seqs)
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

	if env.Type == protocol.MsgHelloAck && env.RequestID == internalMcastInfoReqID {
		var info mcastInfoPayload
		if err := json.Unmarshal(env.Payload, &info); err == nil && info.MulticastIP != "" && info.McastPort > 0 {
			if m.role == core.RoleStudent {
				if len(info.Key) > 0 {
					if c, err := NewFrameCipher(info.Key); err == nil {
						m.mcastCipher.Store(c)
					}
				}
				_ = m.startMulticastReceiver(net.JoinHostPort(info.MulticastIP, strconv.Itoa(info.McastPort)))
			}
		}
		return
	}

	if env.Type == protocol.MsgHelloAck && env.RequestID == internalMcastAckReqID {
		if sm, ok := sess.(*SmuxSession); ok {
			m.mu.Lock()
			if m.sessions[sm.ID()] == sm {
				m.mcastConfirmed[sm] = true
			}
			m.mu.Unlock()
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

// sameRemoteHost reports whether two sessions come from the same remote IP,
// which lets a reconnecting student rebind its alias before the old session
// times out while preventing other hosts from hijacking it.
func sameRemoteHost(a, b *SmuxSession) bool {
	ah, _, errA := net.SplitHostPort(a.RemoteAddr())
	bh, _, errB := net.SplitHostPort(b.RemoteAddr())
	if errA != nil || errB != nil || ah == "" {
		return false
	}
	return ah == bh
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
	m.started = false
	m.starting = false
	m.invalidateConnectionGeneration()
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
