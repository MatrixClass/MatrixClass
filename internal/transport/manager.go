package transport

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
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
		sess, err := NewSmuxSession(conn, true, m.handleEnvelope)
		if err != nil {
			_ = conn.Close()
			continue
		}
		sess.SetOnClose(func(closed core.Session) {
			m.removeSession(closed.ID())
		})
		if !m.addSessionIfCurrent(sess, 0) {
			_ = sess.Close()
			continue
		}
		m.mu.RLock()
		mcastIP := m.beacon.MulticastIP
		mcastPort := m.beacon.McastPort
		m.mu.RUnlock()
		if mcastIP != "" && mcastPort > 0 {
			if raw, err := json.Marshal(mcastInfoPayload{MulticastIP: mcastIP, McastPort: mcastPort}); err == nil {
				_ = sess.SendEnvelope(protocol.Envelope{
					Type:      protocol.MsgHelloAck,
					RequestID: internalMcastInfoReqID,
					Timestamp: time.Now().UnixMilli(),
					Payload:   raw,
				})
			}
		}
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
	m.mu.Unlock()
	return nil
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
	m.mu.RUnlock()

	var discovered DiscoveredTeacher
	if teacherAddr == "" {
		listenAddr := fmt.Sprintf(":%d", dPort)
		if dPort <= 0 {
			listenAddr = fmt.Sprintf(":%d", DefaultDiscoveryPort)
		}
		d, err := ListenDiscovery(ctx, listenAddr, channel)
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

func (m *Manager) startMulticastReceiver(addrStr string) error {
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
		mc, err := listenMulticastAllInterfaces(udpAddr)
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

func listenMulticastAllInterfaces(addr *net.UDPAddr) (net.PacketConn, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var conns []*net.UDPConn
	for _, iface := range usableInterfaces(ifaces) {
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

type multiPacket struct {
	data []byte
	addr net.Addr
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
		if m.cipher != nil {
			dec, err := m.cipher.Decrypt(buf[:n])
			if err != nil {
				continue
			}
			pkt = dec
		}
		if m.processIncomingMulticastPacket(pkt) && m.role == core.RoleStudent && m.mcastAckSent.CompareAndSwap(false, true) {
			if err := m.sendMulticastAck(); err != nil {
				// Retry on the next multicast packet.
				m.mcastAckSent.Store(false)
			}
		}
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
	m.mcastAckSent.Store(false)
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

func (m *Manager) unconfirmedSessionsLocked() []*SmuxSession {
	var out []*SmuxSession
	for _, s := range m.sessions {
		if !m.mcastConfirmed[s] {
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
