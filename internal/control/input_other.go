//go:build !windows && !linux && !darwin

package control

import "errors"

var ErrInputUnsupportedPlatform = errors.New("control: remote input is unsupported on this platform")

type noopInputBackend struct{}

func newPlatformInputBackend() InputBackend {
	return &noopInputBackend{}
}

func (n *noopInputBackend) SendInputs(inputs []WinInput) error {
	return ErrInputUnsupportedPlatform
}
