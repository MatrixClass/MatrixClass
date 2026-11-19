//go:build darwin

package control

import "errors"

var (
	ErrDarwinAccessibilityRequired = errors.New("control: macOS Accessibility permission is required for CGEvent input")
	ErrDarwinInputUnavailable      = errors.New("control: macOS CGEvent input backend is unavailable in this build")
)

type darwinUnavailableInputBackend struct{}

func newPlatformInputBackend() InputBackend {
	return &darwinUnavailableInputBackend{}
}

func (b *darwinUnavailableInputBackend) SendInputs([]WinInput) error {
	return errors.Join(ErrDarwinInputUnavailable, ErrDarwinAccessibilityRequired)
}
