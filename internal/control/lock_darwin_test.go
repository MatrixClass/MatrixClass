//go:build darwin

package control

import (
	"context"
	"errors"
	"testing"
)

func TestDarwinLockBackendUsesNativeCommandBeforeOverlay(t *testing.T) {
	shown := false
	hidden := false
	updated := false
	backend := &darwinLockBackend{command: func(context.Context, string, ...string) error { return nil }, overlays: OverlayHooks{
		OnShow:   func(string) { shown = true },
		OnHide:   func() { hidden = true },
		OnUpdate: func(string) { updated = true },
	}}

	if err := backend.EnableLock("exam"); err != nil {
		t.Fatalf("EnableLock error = %v", err)
	}
	if !shown || hidden || updated {
		t.Fatalf("unexpected overlay hooks: shown=%v hidden=%v updated=%v", shown, hidden, updated)
	}
	if err := backend.UpdateMessage("new message"); err != nil {
		t.Fatalf("UpdateMessage error = %v", err)
	}
	if err := backend.DisableLock(); !errors.Is(err, ErrDarwinUnlockUnavailable) {
		t.Fatalf("DisableLock error = %v, want ErrDarwinUnlockUnavailable", err)
	}
}

func TestDarwinLockBackendSuspendsSessionAndDoesNotClaimUnlock(t *testing.T) {
	var name string
	var args []string
	backend := &darwinLockBackend{
		command: func(_ context.Context, gotName string, gotArgs ...string) error {
			name = gotName
			args = append([]string(nil), gotArgs...)
			return nil
		},
	}
	if err := backend.EnableLock("exam"); err != nil {
		t.Fatalf("EnableLock failed: %v", err)
	}
	if name != darwinCGSessionPath || len(args) != 1 || args[0] != "-suspend" {
		t.Fatalf("unexpected suspend command: %q %v", name, args)
	}
	if err := backend.DisableLock(); !errors.Is(err, ErrDarwinUnlockUnavailable) {
		t.Fatalf("DisableLock error = %v, want ErrDarwinUnlockUnavailable", err)
	}
}

func TestDarwinLockBackendDoesNotInvokeHooksAfterCommandFailure(t *testing.T) {
	shown := false
	backend := &darwinLockBackend{
		overlays: OverlayHooks{OnShow: func(string) { shown = true }},
		command:  func(context.Context, string, ...string) error { return errors.New("permission denied") },
	}
	if err := backend.EnableLock("exam"); err == nil {
		t.Fatal("EnableLock succeeded despite command failure")
	}
	if shown {
		t.Fatal("lock overlay was shown after command failure")
	}
}
