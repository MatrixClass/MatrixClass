package control

import (
	"errors"
	"fmt"
	"net"
	"nexlab/internal/core"
	"time"
)

const (
	WOLMagicPacketSize = 102
	DefaultWOLPort     = 9
)

var (
	ErrInvalidMACAddress   = errors.New("control: invalid MAC address")
	ErrUnsupportedPowerAct = errors.New("control: unsupported power action")
)

type PowerExecutor interface {
	Shutdown(timeoutSec uint32, reason string) error
	Reboot(timeoutSec uint32, reason string) error
	Logoff() error
}

type PowerOptions struct {
	Executor       PowerExecutor
	BroadcastAddr  string
	ShutdownDelay  uint32
	ShutdownReason string
	UDPSender      func(addr string, packet []byte) error
}

type PowerController struct {
	executor       PowerExecutor
	broadcastAddr  string
	shutdownDelay  uint32
	shutdownReason string
	udpSender      func(addr string, packet []byte) error
}

func NewPowerController(opts PowerOptions) *PowerController {
	exec := opts.Executor
	if exec == nil {
		exec = newPlatformPowerExecutor()
	}
	bcast := opts.BroadcastAddr
	if bcast == "" {
		bcast = fmt.Sprintf("255.255.255.255:%d", DefaultWOLPort)
	}
	reason := opts.ShutdownReason
	if reason == "" {
		reason = "NexLab Classroom Management"
	}
	sender := opts.UDPSender
	if sender == nil {
		sender = sendUDPBroadcast
	}
	return &PowerController{
		executor:       exec,
		broadcastAddr:  bcast,
		shutdownDelay:  opts.ShutdownDelay,
		shutdownReason: reason,
		udpSender:      sender,
	}
}

func BuildWOLMagicPacket(macAddr string) ([]byte, error) {
	hw, err := net.ParseMAC(macAddr)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("%w: %q", ErrInvalidMACAddress, macAddr)
	}
	pkt := make([]byte, WOLMagicPacketSize)
	for i := 0; i < 6; i++ {
		pkt[i] = 0xFF
	}
	for rep := 0; rep < 16; rep++ {
		offset := 6 + rep*6
		copy(pkt[offset:offset+6], hw)
	}
	return pkt, nil
}

func (p *PowerController) Execute(action core.PowerAction, macAddr string) error {
	switch action {
	case core.PowerWake:
		pkt, err := BuildWOLMagicPacket(macAddr)
		if err != nil {
			return err
		}
		return p.udpSender(p.broadcastAddr, pkt)
	case core.PowerShutdown:
		return p.executor.Shutdown(p.shutdownDelay, p.shutdownReason)
	case core.PowerReboot:
		return p.executor.Reboot(p.shutdownDelay, p.shutdownReason)
	case core.PowerLogoff:
		return p.executor.Logoff()
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedPowerAct, action)
	}
}

func sendUDPBroadcast(addr string, packet []byte) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp4", nil, udpAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Write(packet)
	if err != nil {
		return err
	}
	if n != len(packet) {
		return fmt.Errorf("control: short WOL packet write: %d/%d", n, len(packet))
	}
	return nil
}
