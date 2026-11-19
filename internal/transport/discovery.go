package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"nexlab/internal/protocol"
)

const (
	DefaultDiscoveryPort     = 39101
	DefaultDiscoveryInterval = 1 * time.Second
)

var (
	ErrDiscoveryTimeout = errors.New("discovery: timed out waiting for teacher beacon")
)

type DiscoveredTeacher struct {
	Beacon protocol.DiscoveryBeacon
	Host   string
	Addr   *net.UDPAddr
}

type DiscoveryConfig struct {
	Port          int
	BroadcastAddr string
	Interval      time.Duration
}

var (
	interfaceEnumerator        = net.Interfaces
	interfaceAddressEnumerator = func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() }
)

func usableInterfaces(interfaces []net.Interface) []net.Interface {
	usable := make([]net.Interface, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&(net.FlagBroadcast|net.FlagMulticast) == 0 {
			continue
		}
		usable = append(usable, iface)
	}
	return usable
}

func DefaultDiscoveryConfig() DiscoveryConfig {
	return DiscoveryConfig{
		Port:          DefaultDiscoveryPort,
		BroadcastAddr: fmt.Sprintf("255.255.255.255:%d", DefaultDiscoveryPort),
		Interval:      DefaultDiscoveryInterval,
	}
}

// BeaconBroadcaster periodically broadcasts a teacher's DiscoveryBeacon over UDP.
type BeaconBroadcaster struct {
	beacon   protocol.DiscoveryBeacon
	targets  []*net.UDPAddr
	port     int
	explicit string
	mu       sync.RWMutex
	interval time.Duration
	conn     *net.UDPConn
	done     chan struct{}
	once     sync.Once
}

func collectBroadcastTargets(port int, explicit string) []*net.UDPAddr {
	seen := make(map[string]struct{})
	var addrs []*net.UDPAddr
	add := func(host string) {
		u, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
		if err != nil {
			return
		}
		key := u.String()
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			addrs = append(addrs, u)
		}
	}

	if explicit != "" {
		if u, err := net.ResolveUDPAddr("udp4", explicit); err == nil {
			seen[u.String()] = struct{}{}
			addrs = append(addrs, u)
			if u.IP.String() != net.IPv4bcast.String() {
				return addrs
			}
		}
	}

	add("255.255.255.255")
	if ifaces, err := interfaceEnumerator(); err == nil {
		for _, iface := range usableInterfaces(ifaces) {
			if iface.Flags&net.FlagBroadcast == 0 {
				continue
			}
			ifAddrs, err := interfaceAddressEnumerator(iface)
			if err != nil {
				continue
			}
			for _, a := range ifAddrs {
				ipNet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				ip4 := ipNet.IP.To4()
				if ip4 == nil || len(ipNet.Mask) != 4 {
					continue
				}
				bcast := make(net.IP, 4)
				for i := 0; i < 4; i++ {
					bcast[i] = ip4[i] | ^ipNet.Mask[i]
				}
				add(bcast.String())
			}
		}
	}
	return addrs
}

func StartBeaconBroadcaster(ctx context.Context, beacon protocol.DiscoveryBeacon, cfg DiscoveryConfig) (*BeaconBroadcaster, error) {
	if cfg.Port <= 0 {
		cfg.Port = DefaultDiscoveryPort
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultDiscoveryInterval
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	targets := collectBroadcastTargets(cfg.Port, cfg.BroadcastAddr)
	b := &BeaconBroadcaster{
		beacon:   beacon,
		targets:  targets,
		port:     cfg.Port,
		explicit: cfg.BroadcastAddr,
		interval: cfg.Interval,
		conn:     conn,
		done:     make(chan struct{}),
	}
	go b.loop(ctx)
	return b, nil
}

func (b *BeaconBroadcaster) loop(ctx context.Context) {
	payload, err := json.Marshal(b.beacon)
	if err != nil {
		return
	}
	send := func() {
		b.refreshTargets()
		b.mu.RLock()
		targets := append([]*net.UDPAddr(nil), b.targets...)
		b.mu.RUnlock()
		for _, target := range targets {
			_, _ = b.conn.WriteToUDP(payload, target)
		}
	}
	send()
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = b.Close()
			return
		case <-b.done:
			return
		case <-ticker.C:
			send()
		}
	}
}

func (b *BeaconBroadcaster) refreshTargets() {
	targets := collectBroadcastTargets(b.port, b.explicit)
	b.mu.Lock()
	b.targets = targets
	b.mu.Unlock()
}

func (b *BeaconBroadcaster) Close() error {
	var err error
	b.once.Do(func() {
		close(b.done)
		if b.conn != nil {
			err = b.conn.Close()
		}
	})
	return err
}

// ListenDiscovery listens on a UDP port for DiscoveryBeacon matching channel (empty channel accepts any).
func ListenDiscovery(ctx context.Context, listenAddr string, channel string) (DiscoveredTeacher, error) {
	if listenAddr == "" {
		listenAddr = fmt.Sprintf(":%d", DefaultDiscoveryPort)
	}
	pc, err := net.ListenPacket("udp4", listenAddr)
	if err != nil {
		return DiscoveredTeacher{}, err
	}
	defer pc.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = pc.Close()
		case <-done:
		}
	}()
	defer close(done)

	buf := make([]byte, 8192)
	for {
		_ = pc.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return DiscoveredTeacher{}, ctx.Err()
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return DiscoveredTeacher{}, err
		}
		var beacon protocol.DiscoveryBeacon
		if err := json.Unmarshal(buf[:n], &beacon); err != nil {
			continue
		}
		if beacon.Role != "" && beacon.Role != "teacher" {
			continue
		}
		if channel != "" && beacon.Channel != channel {
			continue
		}
		udpAddr, _ := addr.(*net.UDPAddr)
		host := ""
		if udpAddr != nil {
			host = udpAddr.IP.String()
		}
		return DiscoveredTeacher{
			Beacon: beacon,
			Host:   host,
			Addr:   udpAddr,
		}, nil
	}
}
