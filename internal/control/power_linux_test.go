//go:build linux

package control

import (
	"context"
	"errors"
	"testing"
)

func TestLinuxPowerExecutorBuildsActionCommand(t *testing.T) {
	var name string
	var args []string
	executor := &linuxPowerExecutor{command: func(_ context.Context, gotName string, gotArgs ...string) error {
		name = gotName
		args = gotArgs
		return nil
	}}
	if err := executor.runLinuxAction("reboot", 0, "classroom"); err != nil {
		t.Fatalf("runLinuxAction failed: %v", err)
	}
	if name == "" || len(args) == 0 {
		t.Fatalf("expected command invocation, got name=%q args=%v", name, args)
	}
}

func TestLinuxPowerCommandFallsBackWithoutSystemctl(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(name string) (string, error) {
		if name == "shutdown" {
			return "/sbin/shutdown", nil
		}
		return "", errors.New("not found")
	}
	name, args, ok := linuxPowerCommand("shutdown", 61, "lesson ended")
	if !ok || name != "/sbin/shutdown" || len(args) != 3 || args[0] != "-h" || args[1] != "+2" || args[2] != "lesson ended" {
		t.Fatalf("unexpected fallback command: name=%q args=%v ok=%v", name, args, ok)
	}
}

func TestLinuxPowerExecutorReportsUnavailableCommand(t *testing.T) {
	original := linuxPowerLookPath
	defer func() { linuxPowerLookPath = original }()
	linuxPowerLookPath = func(string) (string, error) { return "", errors.New("not found") }
	executor := &linuxPowerExecutor{command: func(context.Context, string, ...string) error { return nil }}
	if err := executor.Shutdown(0, ""); !errors.Is(err, ErrPowerUnsupportedPlatform) {
		t.Fatalf("Shutdown error = %v, want ErrPowerUnsupportedPlatform", err)
	}
}
