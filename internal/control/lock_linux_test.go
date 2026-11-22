//go:build linux

package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func stubLinuxLockTools(t *testing.T, tools map[string]string, showUserDisplay string) {
	t.Helper()
	originalLook, originalOutput := linuxLockLookPath, linuxLockOutput
	t.Cleanup(func() { linuxLockLookPath, linuxLockOutput = originalLook, originalOutput })
	linuxLockLookPath = func(name string) (string, error) {
		if path, ok := tools[name]; ok {
			return path, nil
		}
		return "", errLinuxCommandMissing
	}
	linuxLockOutput = func(_ context.Context, name string, args ...string) (string, error) {
		if showUserDisplay == "" {
			return "", errors.New("no display session")
		}
		return showUserDisplay + "\n", nil
	}
}

type recordedLockRun struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]error
}

func (r *recordedLockRun) run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, call)
	for prefix, err := range r.fail {
		if strings.HasPrefix(call, prefix) {
			return err
		}
	}
	return nil
}

func TestLinuxLockCommandPrefersLoginctl(t *testing.T) {
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl", "xdg-screensaver": "/usr/bin/xdg-screensaver"}, "")
	command, ok := linuxLockCommand()
	if !ok || command.name != "/usr/bin/loginctl" || len(command.args) != 1 || command.args[0] != "lock-session" {
		t.Fatalf("unexpected lock command: %+v ok=%v", command, ok)
	}
}

func TestLinuxLockCommandUsesSessionID(t *testing.T) {
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl"}, "")
	command, ok := linuxLockCommandForSession("2")
	if !ok || len(command.args) != 2 || command.args[0] != "lock-session" || command.args[1] != "2" {
		t.Fatalf("unexpected session lock command: %+v ok=%v", command, ok)
	}
}

func TestLinuxUnlockCommandOnlyForLoginctl(t *testing.T) {
	unlock, ok := linuxUnlockCommand(linuxLockCommandSpec{name: "/usr/bin/loginctl", args: []string{"lock-session", "3"}}, "3")
	if !ok || fmt.Sprint(unlock.args) != "[unlock-session 3]" {
		t.Fatalf("unlock = %+v ok=%v", unlock, ok)
	}
	// Regression: the old code compared the full path with "xdg-screensaver"
	// and ran "xdg-screensaver unlock-session".
	if _, ok := linuxUnlockCommand(linuxLockCommandSpec{name: "/usr/bin/xdg-screensaver", args: []string{"lock"}}, ""); ok {
		t.Fatal("xdg-screensaver has no unlock operation")
	}
}

func TestLinuxSessionIDFallsBackToDisplaySession(t *testing.T) {
	t.Setenv("XDG_SESSION_ID", "")
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl"}, "c2")
	if got := linuxSessionID(); got != "c2" {
		t.Fatalf("linuxSessionID = %q, want c2", got)
	}
	t.Setenv("XDG_SESSION_ID", "7")
	if got := linuxSessionID(); got != "7" {
		t.Fatalf("linuxSessionID = %q, want 7", got)
	}
	if linuxValidSessionID("1; rm -rf /") != "" || linuxValidSessionID("") != "" {
		t.Fatal("invalid session ids accepted")
	}
}

func TestLinuxLockDefaultIsOverlayOnly(t *testing.T) {
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl"}, "")
	recorder := &recordedLockRun{}
	var shown, hidden int
	var mu sync.Mutex
	backend := NewLinuxLockBackend(LinuxLockOptions{Overlays: OverlayHooks{
		OnShow: func(string) { mu.Lock(); shown++; mu.Unlock() },
		OnHide: func() { mu.Lock(); hidden++; mu.Unlock() },
	}}).(*linuxLockBackend)
	backend.run = recorder.run
	if err := backend.EnableLock("exam"); err != nil {
		t.Fatal(err)
	}
	if err := backend.DisableLock(); err != nil {
		t.Fatal(err)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("overlay lock ran session commands: %v", recorder.calls)
	}
	if shown != 1 || hidden != 1 {
		t.Fatalf("shown=%d hidden=%d", shown, hidden)
	}
}

func TestLinuxLockEnvOptsIntoSessionLock(t *testing.T) {
	t.Setenv(linuxSessionLockEnv, "1")
	if backend := newPlatformLockBackend(OverlayHooks{}).(*linuxLockBackend); !backend.sessionLock {
		t.Fatal("env opt-in ignored")
	}
	t.Setenv(linuxSessionLockEnv, "")
	if backend := newPlatformLockBackend(OverlayHooks{}).(*linuxLockBackend); backend.sessionLock {
		t.Fatal("session lock enabled by default")
	}
}

func TestLinuxSessionLockAndUnlockTargetSession(t *testing.T) {
	t.Setenv("XDG_SESSION_ID", "5")
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl"}, "")
	recorder := &recordedLockRun{}
	backend := &linuxLockBackend{sessionLock: true, run: recorder.run}
	if err := backend.EnableLock("x"); err != nil {
		t.Fatal(err)
	}
	if err := backend.DisableLock(); err != nil {
		t.Fatal(err)
	}
	want := "[/usr/bin/loginctl lock-session 5 /usr/bin/loginctl unlock-session 5]"
	if fmt.Sprint(recorder.calls) != want {
		t.Fatalf("calls = %v want %v", recorder.calls, want)
	}
}

func TestLinuxLockBackendDoesNotShowOverlayWhenSessionLockFails(t *testing.T) {
	t.Setenv("XDG_SESSION_ID", "5")
	stubLinuxLockTools(t, map[string]string{"loginctl": "/usr/bin/loginctl"}, "")
	shown := false
	backend := &linuxLockBackend{
		sessionLock: true,
		overlays:    OverlayHooks{OnShow: func(string) { shown = true }},
		run: func(context.Context, string, ...string) error {
			return errors.New("permission denied")
		},
	}
	if err := backend.EnableLock("exam"); err == nil {
		t.Fatal("EnableLock succeeded despite command failure")
	}
	if shown || backend.active {
		t.Fatalf("backend claimed lock after command failure: shown=%v active=%v", shown, backend.active)
	}
}

func TestLinuxSessionUnlockFailureStillRemovesOverlay(t *testing.T) {
	t.Setenv("XDG_SESSION_ID", "5")
	stubLinuxLockTools(t, map[string]string{"xdg-screensaver": "/usr/bin/xdg-screensaver"}, "")
	recorder := &recordedLockRun{}
	hidden := false
	backend := &linuxLockBackend{sessionLock: true, run: recorder.run, overlays: OverlayHooks{OnHide: func() { hidden = true }}}
	if err := backend.EnableLock("x"); err != nil {
		t.Fatal(err)
	}
	err := backend.DisableLock()
	if !errors.Is(err, ErrLinuxSessionUnlockFailed) {
		t.Fatalf("DisableLock err = %v", err)
	}
	if !hidden || backend.active {
		t.Fatalf("overlay not removed: hidden=%v active=%v", hidden, backend.active)
	}
	if err := backend.DisableLock(); err != nil {
		t.Fatalf("second DisableLock err = %v", err)
	}
}

func TestLinuxLockDisconnectTimeoutUnlocksOverlay(t *testing.T) {
	hidden := make(chan struct{}, 1)
	backend := NewLinuxLockBackend(LinuxLockOptions{Overlays: OverlayHooks{OnHide: func() { hidden <- struct{}{} }}})
	states := make(chan LockState, 4)
	ctrl := NewScreenLockController(LockOptions{
		Backend:           backend,
		DisconnectTimeout: 30 * time.Millisecond,
		OnStateChange:     func(st LockState) { states <- st },
	})
	defer ctrl.Close()
	if err := ctrl.SetLocked(true, "quiet"); err != nil {
		t.Fatal(err)
	}
	<-states
	select {
	case <-hidden:
	case <-time.After(5 * time.Second):
		t.Fatal("overlay not hidden after disconnect timeout")
	}
	select {
	case st := <-states:
		if st.Locked || st.UnlockReason != UnlockReasonDisconnectTimeout {
			t.Fatalf("state = %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no state change after disconnect timeout")
	}
}
