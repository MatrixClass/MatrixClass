//go:build !windows

package control

import "errors"

var ErrPowerUnsupportedPlatform = errors.New("control: local OS power actions are only supported on Windows")

type noopPowerExecutor struct{}

func newPlatformPowerExecutor() PowerExecutor {
	return &noopPowerExecutor{}
}

func (n *noopPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	return ErrPowerUnsupportedPlatform
}

func (n *noopPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	return ErrPowerUnsupportedPlatform
}

func (n *noopPowerExecutor) Logoff() error {
	return ErrPowerUnsupportedPlatform
}
