package transport

import (
	"net"
	"testing"
)

func TestNetworkConfigIgnoresVirtualAndVPNInterfaces(t *testing.T) {
	interfaces := []net.Interface{
		{Name: "Ethernet", Flags: net.FlagUp | net.FlagBroadcast},
		{Name: "VMware Network Adapter VMnet8", Flags: net.FlagUp | net.FlagBroadcast},
		{Name: "WireGuard Tunnel", Flags: net.FlagUp | net.FlagBroadcast},
		{Name: "Meta", Flags: net.FlagUp | net.FlagBroadcast},
	}
	got := usableInterfacesWithConfig(interfaces, NetworkConfig{Mode: NetworkModeMulti, IgnoreVirtual: true, IgnoreVPN: true})
	if len(got) != 1 || got[0].Name != "Ethernet" {
		t.Fatalf("filtered interfaces = %#v, want Ethernet only", got)
	}
}

func TestNetworkConfigSingleModeKeepsSelectedInterface(t *testing.T) {
	interfaces := []net.Interface{
		{Name: "Ethernet", Flags: net.FlagUp | net.FlagBroadcast},
		{Name: "Wi-Fi", Flags: net.FlagUp | net.FlagBroadcast},
	}
	got := usableInterfacesWithConfig(interfaces, NetworkConfig{Mode: NetworkModeSingle, InterfaceName: "Wi-Fi"})
	if len(got) != 1 || got[0].Name != "Wi-Fi" {
		t.Fatalf("single-interface selection = %#v, want Wi-Fi only", got)
	}
}
