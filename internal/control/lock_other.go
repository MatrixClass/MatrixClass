//go:build !windows && !linux && !darwin

package control

type noopLockBackend struct {
	overlays OverlayHooks
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &noopLockBackend{overlays: overlays}
}

func (n *noopLockBackend) EnableLock(message string) error {
	if n.overlays.OnShow != nil {
		n.overlays.OnShow(message)
	}
	return nil
}

func (n *noopLockBackend) DisableLock() error {
	if n.overlays.OnHide != nil {
		n.overlays.OnHide()
	}
	return nil
}

func (n *noopLockBackend) UpdateMessage(message string) error {
	if n.overlays.OnUpdate != nil {
		n.overlays.OnUpdate(message)
	}
	return nil
}
