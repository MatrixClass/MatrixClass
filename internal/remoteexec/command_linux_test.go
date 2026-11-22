//go:build linux

package remoteexec

import (
	"context"
	"testing"
	"time"
)

func TestShellTimeoutKillsProcessGroup(t *testing.T) {
	start := time.Now()
	task, err := NewService().StartCommand(context.Background(), CommandRequest{RequestID: "t", Shell: ShellSH, Command: "sleep 30 | cat", TimeoutSec: 1}, nil)
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	select {
	case result := <-task.Result:
		if result.Status != "timeout" {
			t.Fatalf("expected timeout, got %+v", result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("command did not finish; process group not killed")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}
