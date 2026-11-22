//go:build darwin

package control

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// macOS classroom lock.
//
// The lock is overlay based and fully reversible by this process:
//   - the student UI shows its full-screen, always-on-top lock window
//     (ModeLock) and the optional OverlayHooks are invoked;
//   - NSApplication kiosk presentation options hide the Dock/menu bar and
//     disable app switching, Force Quit, log out/restart and Hide;
//   - a CGEventTap (Accessibility permission) swallows Cmd/Ctrl shortcuts.
//
// It deliberately never suspends/locks the login session (CGSession
// -suspend, pmset displaysleepnow, ...): the app cannot undo those, so a
// teacher unlock or the disconnect auto-unlock would leave the student at the
// login window. Native pieces are best effort: EnableLock succeeds even when
// kiosk/guard setup fails, so the lock controller keeps its state, heartbeat
// timeout and emergency unlock working. LastError exposes the degradation.

var (
	// ErrDarwinLockUnavailable is kept for API compatibility; the overlay
	// lock no longer depends on an OS lock command.
	ErrDarwinLockUnavailable = errors.New("control: macOS session lock is unavailable")
	// ErrDarwinUnlockUnavailable is kept for API compatibility; DisableLock
	// now always releases the overlay lock.
	ErrDarwinUnlockUnavailable = errors.New("control: macOS session unlock is not available to classroom control")
)

type darwinLockBackend struct {
	mu       sync.Mutex
	active   bool
	overlays OverlayHooks
	kiosk    darwinKiosk
	guard    darwinShortcutGuard
	lastErr  error
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &darwinLockBackend{overlays: overlays, kiosk: newDarwinPresentationKiosk(), guard: newDarwinEventTapGuard()}
}

// SetOverlayHooks replaces the overlay callbacks (mirrors WindowsLockBackend).
func (b *darwinLockBackend) SetOverlayHooks(overlays OverlayHooks) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.overlays = overlays
}

// LastError reports why the most recent lock/unlock ran degraded (nil when
// kiosk mode and the shortcut guard were both applied/released).
func (b *darwinLockBackend) LastError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastErr
}

func (b *darwinLockBackend) EnableLock(message string) error {
	b.mu.Lock()
	if b.active {
		overlays := b.overlays
		b.mu.Unlock()
		if overlays.OnUpdate != nil {
			overlays.OnUpdate(message)
		}
		return nil
	}
	b.active = true
	kiosk, guard, overlays := b.kiosk, b.guard, b.overlays
	var errs []error
	if kiosk != nil {
		if err := kiosk.Enter(); err != nil {
			errs = append(errs, fmt.Errorf("kiosk: %w", err))
		}
	}
	if guard != nil {
		if err := guard.Start(); err != nil {
			errs = append(errs, fmt.Errorf("shortcut guard: %w", err))
		}
	}
	b.lastErr = errors.Join(errs...)
	b.mu.Unlock()
	if overlays.OnShow != nil {
		overlays.OnShow(message)
	}
	return nil
}

func (b *darwinLockBackend) DisableLock() error {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return nil
	}
	b.active = false
	kiosk, guard, overlays := b.kiosk, b.guard, b.overlays
	// Release input first so the student regains shortcuts even if
	// restoring presentation options fails.
	if guard != nil {
		guard.Stop()
	}
	var err error
	if kiosk != nil {
		if exitErr := kiosk.Exit(); exitErr != nil {
			err = fmt.Errorf("control: restore macOS presentation options: %w", exitErr)
		}
	}
	b.lastErr = err
	b.mu.Unlock()
	if overlays.OnHide != nil {
		overlays.OnHide()
	}
	// The lock itself is released at this point; a failure to restore the
	// presentation options is recorded in LastError (the kiosk retries in the
	// background) rather than returned, so the controller still reports the
	// unlock to the teacher.
	return nil
}

func (b *darwinLockBackend) UpdateMessage(message string) error {
	b.mu.Lock()
	overlays := b.overlays
	b.mu.Unlock()
	if overlays.OnUpdate != nil {
		overlays.OnUpdate(message)
	}
	return nil
}

func runDarwinCommand(parent context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(parent, name, args...).CombinedOutput()
	if err != nil {
		if text := strings.TrimSpace(string(output)); text != "" {
			return fmt.Errorf("%w: %s", err, text)
		}
		return err
	}
	return nil
}
