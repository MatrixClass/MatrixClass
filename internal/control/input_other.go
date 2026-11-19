//go:build !windows && !linux

package control

import "errors"

var ErrInputUnsupportedPlatform = errors.New("control: SendInput is only supported on Windows")

type noopInputBackend struct{}

func newPlatformInputBackend() InputBackend {
	return &noopInputBackend{}
}

func (n *noopInputBackend) SendInputs(inputs []WinInput) error {
	return ErrInputUnsupportedPlatform
}
