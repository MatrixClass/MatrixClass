//go:build linux

package control

import (
	"context"
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
