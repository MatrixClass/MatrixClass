//go:build linux

package control

import (
	"errors"
	"os/exec"
	"sync"
)

var (
	ErrLinuxLockUnavailable = errors.New("control: Linux desktop lock command is unavailable")
	errLinuxCommandMissing  = errors.New("command missing")
	linuxLockLookPath       = exec.LookPath
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
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &linuxLockBackend{overlays: overlays}
}

func linuxLockCommand() (linuxLockCommandSpec, bool) {
	if path, err := linuxLockLookPath("loginctl"); err == nil {
		return linuxLockCommandSpec{name: path, args: []string{"lock-session"}}, true
	}
	if path, err := linuxLockLookPath("xdg-screensaver"); err == nil {
		return linuxLockCommandSpec{name: path, args: []string{"lock"}}, true
	}
	return linuxLockCommandSpec{}, false
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
	command, ok := linuxLockCommand()
	if !ok {
		b.mu.Unlock()
		return ErrLinuxLockUnavailable
	}
	b.command = command
	b.mu.Unlock()
	if err := exec.Command(command.name, command.args...).Run(); err != nil {
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
	command.args = []string{"unlock-session"}
	if command.name != "" && command.name != "xdg-screensaver" {
		if err := exec.Command(command.name, command.args...).Run(); err != nil {
			b.mu.Unlock()
			return err
		}
	} else if command.name == "xdg-screensaver" {
		command.args = []string{"reset"}
		if err := exec.Command(command.name, command.args...).Run(); err != nil {
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
