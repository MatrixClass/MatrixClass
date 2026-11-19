//go:build darwin

package control

import (
	"errors"
	"testing"
)

func TestDarwinLockBackendDoesNotReportLockSuccessWithoutNativeSupport(t *testing.T) {
	shown := false
	hidden := false
	updated := false
	backend := newPlatformLockBackend(OverlayHooks{
		OnShow:   func(string) { shown = true },
		OnHide:   func() { hidden = true },
		OnUpdate: func(string) { updated = true },
	})

	if err := backend.EnableLock("exam"); !errors.Is(err, ErrDarwinLockUnavailable) {
		t.Fatalf("EnableLock error = %v, want ErrDarwinLockUnavailable", err)
	}
	if shown || hidden || updated {
		t.Fatalf("unavailable lock backend invoked overlay hooks: shown=%v hidden=%v updated=%v", shown, hidden, updated)
	}
	if err := backend.UpdateMessage("new message"); !errors.Is(err, ErrDarwinLockUnavailable) {
		t.Fatalf("UpdateMessage error = %v, want ErrDarwinLockUnavailable", err)
	}
	if err := backend.DisableLock(); err != nil {
		t.Fatalf("DisableLock error = %v, want nil for inactive backend", err)
	}
}
