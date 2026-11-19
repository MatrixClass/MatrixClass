//go:build darwin

package control

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
)

const darwinCGSessionPath = "/System/Library/CoreServices/Menu Extras/User.menu/Contents/Resources/CGSession"

var (
	ErrDarwinLockUnavailable   = errors.New("control: macOS session lock is unavailable")
	ErrDarwinUnlockUnavailable = errors.New("control: macOS session unlock is not available to classroom control")
)

type darwinLockBackend struct {
	mu       sync.Mutex
	active   bool
	overlays OverlayHooks
	command  func(context.Context, string, ...string) error
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &darwinLockBackend{overlays: overlays, command: runDarwinCommand}
}

func (b *darwinLockBackend) EnableLock(message string) error {
	b.mu.Lock()
	if b.active {
		b.mu.Unlock()
		return b.UpdateMessage(message)
	}
	command := b.command
	b.mu.Unlock()
	if err := command(context.Background(), darwinCGSessionPath, "-suspend"); err != nil {
		return fmt.Errorf("%w: %v", ErrDarwinLockUnavailable, err)
	}
	b.mu.Lock()
	b.active = true
	overlays := b.overlays
	b.mu.Unlock()
	if overlays.OnShow != nil {
		overlays.OnShow(message)
	}
	return nil
}

func (b *darwinLockBackend) DisableLock() error {
	b.mu.Lock()
	active := b.active
	b.active = false
	overlays := b.overlays
	b.mu.Unlock()
	if !active {
		return nil
	}
	if overlays.OnHide != nil {
		overlays.OnHide()
	}
	return ErrDarwinUnlockUnavailable
}

func (b *darwinLockBackend) UpdateMessage(message string) error {
	b.mu.Lock()
	active := b.active
	overlays := b.overlays
	b.mu.Unlock()
	if !active {
		return ErrDarwinLockUnavailable
	}
	if overlays.OnUpdate != nil {
		overlays.OnUpdate(message)
	}
	return nil
}

func runDarwinCommand(parent context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(parent, name, args...).CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w: %s", err, string(output))
		}
		return err
	}
	return nil
}
