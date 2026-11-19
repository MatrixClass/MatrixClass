//go:build linux

package control

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxLockCommandPrefersLoginctl(t *testing.T) {
	original := linuxLockLookPath
	defer func() { linuxLockLookPath = original }()
	linuxLockLookPath = func(name string) (string, error) {
		if name == "loginctl" {
			return "/usr/bin/loginctl", nil
		}
		return "", errLinuxCommandMissing
	}
	command, ok := linuxLockCommand()
	if !ok || command.name != "/usr/bin/loginctl" || len(command.args) != 1 || command.args[0] != "lock-session" {
		t.Fatalf("unexpected lock command: %+v ok=%v", command, ok)
	}
}

func TestLinuxLockCommandUsesSessionID(t *testing.T) {
	original := linuxLockLookPath
	defer func() { linuxLockLookPath = original }()
	linuxLockLookPath = func(name string) (string, error) {
		if name == "loginctl" {
			return "/usr/bin/loginctl", nil
		}
		return "", errLinuxCommandMissing
	}
	command, ok := linuxLockCommandForSession("2")
	if !ok || len(command.args) != 2 || command.args[0] != "lock-session" || command.args[1] != "2" {
		t.Fatalf("unexpected session lock command: %+v ok=%v", command, ok)
	}
}

func TestLinuxLockBackendDoesNotShowOverlayWhenCommandFails(t *testing.T) {
	shown := false
	backend := &linuxLockBackend{
		overlays: OverlayHooks{OnShow: func(string) { shown = true }},
		command:  linuxLockCommandSpec{name: "loginctl", args: []string{"lock-session"}},
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
