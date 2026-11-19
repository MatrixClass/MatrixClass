package transport

import (
	"net"
	"testing"
)

func TestUsableInterfacesFiltersDownAndLoopback(t *testing.T) {
	interfaces := []net.Interface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback | net.FlagMulticast},
		{Name: "down0", Flags: net.FlagBroadcast | net.FlagMulticast},
		{Name: "eth0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast},
		{Name: "tun0", Flags: net.FlagUp | net.FlagMulticast},
	}

	got := usableInterfaces(interfaces)
	if len(got) != 2 || got[0].Name != "eth0" || got[1].Name != "tun0" {
		t.Fatalf("usableInterfaces() = %#v, want eth0 and tun0", got)
	}
}

func TestCollectBroadcastTargetsUsesCurrentInterfaces(t *testing.T) {
	previous := interfaceEnumerator
	defer func() { interfaceEnumerator = previous }()
	interfaceEnumerator = func() ([]net.Interface, error) {
		return []net.Interface{{
			Name:  "eth0",
			Flags: net.FlagUp | net.FlagBroadcast,
		}}, nil
	}

	previousAddrs := interfaceAddressEnumerator
	defer func() { interfaceAddressEnumerator = previousAddrs }()
	interfaceAddressEnumerator = func(iface net.Interface) ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.10.5"), Mask: net.CIDRMask(24, 32)}}, nil
	}

	targets := collectBroadcastTargets(39101, "")
	if !containsUDPHost(targets, "192.168.10.255") {
		t.Fatalf("broadcast targets do not contain current interface subnet: %#v", targets)
	}
	if containsUDPHost(targets, "127.0.0.1") {
		t.Fatalf("broadcast targets unexpectedly contain loopback")
	}
}

func TestManagerNetworkStatusReportsReconnectReason(t *testing.T) {
	m := NewManager()
	m.recordReconnect("network interface changed")
	status := m.NetworkStatus()
	if status.Reconnects != 1 {
		t.Fatalf("Reconnects = %d, want 1", status.Reconnects)
	}
	if status.LastReconnectReason != "network interface changed" {
		t.Fatalf("LastReconnectReason = %q", status.LastReconnectReason)
	}
}

func containsUDPHost(targets []*net.UDPAddr, host string) bool {
	for _, target := range targets {
		if target.IP.String() == host {
			return true
		}
	}
	return false
}
