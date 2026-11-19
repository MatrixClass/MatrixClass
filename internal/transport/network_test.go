package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"nexlab/internal/core"
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
	previousInterfaces := interfaceEnumerator
	previousAddresses := interfaceAddressEnumerator
	defer func() {
		interfaceEnumerator = previousInterfaces
		interfaceAddressEnumerator = previousAddresses
	}()
	interfaceEnumerator = func() ([]net.Interface, error) {
		return []net.Interface{{Index: 2, Name: "eth0", Flags: net.FlagUp | net.FlagBroadcast}}, nil
	}
	interfaceAddressEnumerator = func(net.Interface) ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("10.0.0.5"), Mask: net.CIDRMask(24, 32)}}, nil
	}
	m := NewManager()
	m.recordReconnect("network interface changed")
	m.recordConnection("10.0.0.8:39101", "10.0.0.5:49210", []string{"2:eth0"}, []string{"10.0.0.5"})
	status := m.NetworkStatus()
	if status.Reconnects != 1 {
		t.Fatalf("Reconnects = %d, want 1", status.Reconnects)
	}
	if status.LastReconnectReason != "network interface changed" {
		t.Fatalf("LastReconnectReason = %q", status.LastReconnectReason)
	}
	if status.ConnectionAddress != "10.0.0.8:39101" {
		t.Fatalf("ConnectionAddress = %q", status.ConnectionAddress)
	}
	if len(status.InterfaceDetails) != 1 || status.InterfaceDetails[0].Name != "eth0" {
		t.Fatalf("InterfaceDetails = %#v", status.InterfaceDetails)
	}
	if len(status.LocalAddresses) != 1 || status.LocalAddresses[0] != "10.0.0.5" {
		t.Fatalf("LocalAddresses = %#v", status.LocalAddresses)
	}
	if status.LocalAddress != "10.0.0.5:49210" {
		t.Fatalf("LocalAddress = %q", status.LocalAddress)
	}
	if status.LastConnectedAt.IsZero() {
		t.Fatal("LastConnectedAt was not recorded")
	}
}

func TestReconnectGenerationRejectsStaleTask(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.role = core.RoleStudent
	m.studentChannel = "class"
	first := m.nextConnectionGeneration()
	second := m.nextConnectionGeneration()
	cancel()
	if first == second {
		t.Fatalf("connection generations did not advance: %d", first)
	}
	if m.connectionGenerationCurrent(first) {
		t.Fatalf("stale generation %d was accepted after generation %d", first, second)
	}
	if !m.connectionGenerationCurrent(second) {
		t.Fatalf("current generation %d was rejected", second)
	}
	m.invalidateConnectionGeneration()
	if m.connectionGenerationCurrent(second) {
		t.Fatalf("generation %d remained current after invalidation", second)
	}
}

func TestConnectionStatusCopiesSlices(t *testing.T) {
	m := NewManager()
	m.recordConnection("teacher:1", "192.168.1.2:1234", []string{"1:eth0"}, []string{"192.168.1.2"})
	status := m.NetworkStatus()
	status.Interfaces[0] = "mutated"
	status.LocalAddresses[0] = "mutated"
	status.InterfaceDetails[0].Addresses[0] = "mutated"
	status = m.NetworkStatus()
	if status.Interfaces[0] == "mutated" || status.LocalAddresses[0] == "mutated" || status.InterfaceDetails[0].Addresses[0] == "mutated" {
		t.Fatal("NetworkStatus returned mutable internal slices")
	}
	if status.LastConnectedAt.After(time.Now()) {
		t.Fatal("LastConnectedAt is in the future")
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
