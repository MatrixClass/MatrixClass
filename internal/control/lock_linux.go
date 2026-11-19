//go:build linux

package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var (
	ErrLinuxLockUnavailable = errors.New("control: Linux desktop lock command is unavailable")
	errLinuxCommandMissing  = errors.New("command missing")
	linuxLockLookPath       = exec.LookPath
	linuxLockRun            = runLinuxLockCommand
)

type linuxLockCommandSpec struct {
	name string
	args []string
}

type linuxLockBackend struct {
	mu       sync.Mutex
	active   bool
	overlays OverlayHooks
	command  linuxLockCommandSpec
	run      func(context.Context, string, ...string) error
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &linuxLockBackend{overlays: overlays, run: linuxLockRun}
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

func runLinuxLockCommand(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil && len(output) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return err
}

func linuxSessionID() string {
	if value := strings.TrimSpace(os.Getenv("XDG_SESSION_ID")); value != "" {
		return value
	}
	return ""
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
	command, ok := linuxLockCommandForSession(linuxSessionID())
	if !ok {
		b.mu.Unlock()
		return ErrLinuxLockUnavailable
	}
	b.command = command
	run := b.run
	if run == nil {
		run = linuxLockRun
	}
	b.mu.Unlock()
	if err := run(context.Background(), command.name, command.args...); err != nil {
		return err
	}
	b.mu.Lock()
	b.active = true
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

func (b *linuxLockBackend) DisableLock() error {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return nil
	}
	command := b.command
	run := b.run
	if run == nil {
		run = linuxLockRun
	}
	command.args = []string{"unlock-session"}
	if command.name != "" && command.name != "xdg-screensaver" {
		if err := run(context.Background(), command.name, command.args...); err != nil {
			b.mu.Unlock()
			return err
		}
	} else if command.name == "xdg-screensaver" {
		command.args = []string{"reset"}
		if err := run(context.Background(), command.name, command.args...); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	b.active = false
	overlay := b.overlays.OnHide
	b.mu.Unlock()
	if overlay != nil {
		overlay()
	}
	return nil
}
