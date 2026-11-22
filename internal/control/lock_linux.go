//go:build linux

package control

// Linux classroom lock.
//
// The classroom lock is an application overlay: the student UI shows a
// fullscreen, always-on-top, non-closable window (OverlayHooks / student
// ModeLock) that the app can always remove again, on teacher unlock, emergency
// password or the disconnect-timeout auto-unlock in lock.go.
//
// The desktop session lock (loginctl lock-session / xdg-screensaver lock) is
// NOT used by default: its lock screen can only be dismissed with the
// student's own password on desktops that ignore `loginctl unlock-session`
// (and xdg-screensaver has no unlock at all), so the teacher or the
// disconnect timeout could not undo it. It is available as an explicit
// opt-in extra (LinuxLockOptions.SessionLock or MATRIXCLASS_LINUX_SESSION_LOCK=1).
//
// Limits: Wayland does not let a client grab the keyboard globally or keep a
// window above the compositor's own UI, so desktop shortcuts (Super,
// Alt+Tab, workspace switching, the GNOME activities overview, KDE/UKUI/DDE
// launchers, Ctrl+Alt+F<n> VT switches) remain usable; the overlay is only as
// strong as the compositor's fullscreen/keep-above handling. On X11 the window
// manager already owns these key grabs, so the overlay cannot block them
// either without breaking keyboard input to the overlay's emergency-password
// field. The lock is therefore a classroom attention tool, not a security
// boundary.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// linuxSessionLockEnv opts the default Linux lock backend into additionally
// locking the desktop session.
const linuxSessionLockEnv = "MATRIXCLASS_LINUX_SESSION_LOCK"

var (
	ErrLinuxLockUnavailable = errors.New("control: Linux desktop lock command is unavailable")
	// ErrLinuxSessionUnlockFailed is returned by DisableLock when the
	// optional desktop session lock could not be released; the classroom
	// overlay is still removed and the student must unlock the desktop
	// lock screen with their own password.
	ErrLinuxSessionUnlockFailed = errors.New("control: the desktop session lock could not be released; the student must unlock it with their password")
	errLinuxCommandMissing      = errors.New("command missing")
	linuxLockLookPath           = exec.LookPath
	linuxLockRun                = runLinuxLockCommand
	linuxLockOutput             = runLinuxLockOutput
)

const linuxLockCommandTimeout = 10 * time.Second

type linuxLockCommandSpec struct {
	name string
	args []string
}

// LinuxLockOptions configures NewLinuxLockBackend.
type LinuxLockOptions struct {
	Overlays OverlayHooks
	// SessionLock additionally locks the desktop session (loginctl
	// lock-session, falling back to xdg-screensaver lock). Unlock is only
	// best effort: see ErrLinuxSessionUnlockFailed.
	SessionLock bool
}

type linuxLockBackend struct {
	mu          sync.Mutex
	active      bool
	overlays    OverlayHooks
	sessionLock bool
	command     linuxLockCommandSpec
	sessionID   string
	run         func(context.Context, string, ...string) error
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &linuxLockBackend{overlays: overlays, run: linuxLockRun, sessionLock: os.Getenv(linuxSessionLockEnv) == "1"}
}

// NewLinuxLockBackend returns the Linux classroom lock backend (pass it as
// LockOptions.Backend). By default it only drives the application overlay.
func NewLinuxLockBackend(opts LinuxLockOptions) LockBackend {
	return &linuxLockBackend{overlays: opts.Overlays, run: linuxLockRun, sessionLock: opts.SessionLock}
}

func linuxLockCommand() (linuxLockCommandSpec, bool) {
	return linuxLockCommandForSession("")
}

func linuxLockCommandForSession(sessionID string) (linuxLockCommandSpec, bool) {
	if path, err := linuxLockLookPath("loginctl"); err == nil {
		args := []string{"lock-session"}
		if sessionID != "" {
			args = append(args, sessionID)
		}
		return linuxLockCommandSpec{name: path, args: args}, true
	}
	if path, err := linuxLockLookPath("xdg-screensaver"); err == nil {
		return linuxLockCommandSpec{name: path, args: []string{"lock"}}, true
	}
	return linuxLockCommandSpec{}, false
}

// linuxUnlockCommand returns the command that releases a lock taken with
// lockCommand, or false when the lock tool has no unlock operation.
func linuxUnlockCommand(lockCommand linuxLockCommandSpec, sessionID string) (linuxLockCommandSpec, bool) {
	if lockCommand.name == "" || filepath.Base(lockCommand.name) != "loginctl" {
		return linuxLockCommandSpec{}, false
	}
	args := []string{"unlock-session"}
	if sessionID != "" {
		args = append(args, sessionID)
	}
	return linuxLockCommandSpec{name: lockCommand.name, args: args}, true
}

func runLinuxLockCommand(parent context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, linuxLockCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil && len(output) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return err
}

func runLinuxLockOutput(parent context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, linuxLockCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(output)), err
}

// linuxSessionID returns the logind session to lock: XDG_SESSION_ID when the
// app runs inside the session, otherwise (systemd user service, autostart via
// a service manager) the user's graphical "Display" session, so a
// multi-session machine never locks some other session of the user.
func linuxSessionID() string {
	if value := strings.TrimSpace(os.Getenv("XDG_SESSION_ID")); value != "" {
		return value
	}
	path, err := linuxLockLookPath("loginctl")
	if err != nil {
		return ""
	}
	output, err := linuxLockOutput(context.Background(), path, "show-user", strconv.Itoa(os.Getuid()), "--property=Display", "--value")
	if err != nil {
		return ""
	}
	return linuxValidSessionID(output)
}

func linuxValidSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_') {
			return ""
		}
	}
	return value
}

func (b *linuxLockBackend) EnableLock(message string) error {
	b.mu.Lock()
	if b.active {
		overlay := b.overlays.OnUpdate
		b.mu.Unlock()
		if overlay != nil {
			overlay(message)
		}
		return nil
	}
	sessionLock := b.sessionLock
	run := b.run
	if run == nil {
		run = linuxLockRun
	}
	b.mu.Unlock()

	var command linuxLockCommandSpec
	var sessionID string
	if sessionLock {
		sessionID = linuxSessionID()
		var ok bool
		command, ok = linuxLockCommandForSession(sessionID)
		if !ok {
			return ErrLinuxLockUnavailable
		}
		if err := run(context.Background(), command.name, command.args...); err != nil {
			return fmt.Errorf("control: lock desktop session: %w", err)
		}
	}

	b.mu.Lock()
	b.active = true
	b.command = command
	b.sessionID = sessionID
	overlay := b.overlays.OnShow
	b.mu.Unlock()
	if overlay != nil {
		overlay(message)
	}
	return nil
}

func (b *linuxLockBackend) UpdateMessage(message string) error {
	b.mu.Lock()
	overlay := b.overlays.OnUpdate
	b.mu.Unlock()
	if overlay != nil {
		overlay(message)
	}
	return nil
}

// DisableLock always removes the overlay and clears the active state, so the
// app can never be stuck in a "locked" state it cannot leave. A failure to
// release the optional desktop session lock is reported afterwards.
func (b *linuxLockBackend) DisableLock() error {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return nil
	}
	command := b.command
	sessionID := b.sessionID
	run := b.run
	if run == nil {
		run = linuxLockRun
	}
	b.active = false
	b.command = linuxLockCommandSpec{}
	b.sessionID = ""
	overlay := b.overlays.OnHide
	b.mu.Unlock()

	if overlay != nil {
		overlay()
	}
	if command.name == "" {
		return nil
	}
	unlock, ok := linuxUnlockCommand(command, sessionID)
	if !ok {
		return fmt.Errorf("%w (%s has no unlock operation)", ErrLinuxSessionUnlockFailed, filepath.Base(command.name))
	}
	if err := run(context.Background(), unlock.name, unlock.args...); err != nil {
		return fmt.Errorf("%w: %v", ErrLinuxSessionUnlockFailed, err)
	}
	return nil
}
