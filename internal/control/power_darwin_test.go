//go:build darwin

package control

import (
	"context"
	"testing"
)

func TestDarwinPowerExecutorUsesPublicCommands(t *testing.T) {
	var calls []struct {
		name string
		args []string
	}
	executor := &darwinPowerExecutor{
		command: func(_ context.Context, name string, args ...string) error {
			calls = append(calls, struct {
				name string
				args []string
			}{name: name, args: append([]string(nil), args...)})
			return nil
		},
	}
	if err := executor.Shutdown(30, "classroom"); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
	if err := executor.Reboot(0, ""); err != nil {
		t.Fatalf("Reboot failed: %v", err)
	}
	if err := executor.Logoff(); err != nil {
		t.Fatalf("Logoff failed: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %+v, want 3 calls", calls)
	}
	if calls[0].name != "shutdown" || calls[0].args[0] != "-h" || calls[0].args[1] != "+30" {
		t.Fatalf("unexpected shutdown call: %+v", calls[0])
	}
	if calls[1].name != "shutdown" || calls[1].args[0] != "-r" || calls[1].args[1] != "now" {
		t.Fatalf("unexpected reboot call: %+v", calls[1])
	}
	if calls[2].name != "osascript" || len(calls[2].args) < 2 {
		t.Fatalf("unexpected logoff call: %+v", calls[2])
	}
}
