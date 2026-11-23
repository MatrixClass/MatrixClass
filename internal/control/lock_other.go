//go:build !windows && !linux && !darwin

package control

import "errors"

var ErrLockUnsupportedPlatform = errors.New("control: screen lock is unsupported on this platform")

type noopLockBackend struct {
	overlays OverlayHooks
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &noopLockBackend{overlays: overlays}
}

func (n *noopLockBackend) EnableLock(message string) error {
	return ErrLockUnsupportedPlatform
}

func (n *noopLockBackend) DisableLock() error {
	return ErrLockUnsupportedPlatform
}

func (n *noopLockBackend) UpdateMessage(message string) error {
	return ErrLockUnsupportedPlatform
}
