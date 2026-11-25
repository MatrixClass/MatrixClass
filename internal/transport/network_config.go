package transport

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type NetworkMode string

const (
	NetworkModeMulti  NetworkMode = "multi"
	NetworkModeSingle NetworkMode = "single"
)

type NetworkConfig struct {
	Mode          NetworkMode `json:"mode"`
	InterfaceName string      `json:"interface_name,omitempty"`
	IgnoreVirtual bool        `json:"ignore_virtual"`
	IgnoreVPN     bool        `json:"ignore_vpn"`
}

func DefaultNetworkConfig() NetworkConfig {
	return NetworkConfig{Mode: NetworkModeMulti, IgnoreVirtual: true, IgnoreVPN: true}
}

func (c NetworkConfig) normalized() NetworkConfig {
	if c.Mode != NetworkModeSingle {
		c.Mode = NetworkModeMulti
	}
	return c
}

func interfaceIgnored(iface net.Interface, cfg NetworkConfig) bool {
	cfg = cfg.normalized()
	name := strings.ToLower(iface.Name)
	if cfg.Mode == NetworkModeSingle && cfg.InterfaceName != "" && iface.Name != cfg.InterfaceName {
		return true
	}
	if cfg.IgnoreVPN && containsAny(name, "vpn", "tun", "tap", "ppp", "wireguard", "zerotier", "tailscale", "clash") {
		return true
	}
	if cfg.IgnoreVPN && (name == "meta" || containsAny(name, "wintun", "mihomo", "sing-box", "singbox")) {
		return true
	}
	if cfg.IgnoreVirtual && containsAny(name, "vmware", "virtual", "vbox", "hyper-v", "vethernet", "docker", "podman", "br-", "virbr") {
		return true
	}
	return false
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func discoveryListenAddr(port int, cfg NetworkConfig) string {
	if port <= 0 {
		port = DefaultDiscoveryPort
	}
	if cfg.Mode != NetworkModeSingle || cfg.InterfaceName == "" {
		return ":" + strconv.Itoa(port)
	}
	iface, err := net.InterfaceByName(cfg.InterfaceName)
	if err != nil {
		return ":" + strconv.Itoa(port)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ":" + strconv.Itoa(port)
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
			return net.JoinHostPort(ipNet.IP.To4().String(), fmt.Sprintf("%d", port))
		}
	}
	return ":" + strconv.Itoa(port)
}
