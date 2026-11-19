//go:build darwin

package control

import "errors"

var ErrDarwinLockUnavailable = errors.New("control: macOS lock backend is unavailable in this build")

type darwinUnavailableLockBackend struct{}

func newPlatformLockBackend(OverlayHooks) LockBackend {
	return &darwinUnavailableLockBackend{}
}

func (b *darwinUnavailableLockBackend) EnableLock(string) error {
	return ErrDarwinLockUnavailable
}

func (b *darwinUnavailableLockBackend) DisableLock() error {
	return nil
}

func (b *darwinUnavailableLockBackend) UpdateMessage(string) error {
	return ErrDarwinLockUnavailable
}
