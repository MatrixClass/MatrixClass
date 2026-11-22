//go:build windows

package remoteexec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func runForTest(t *testing.T, req CommandRequest) (CommandResult, string) {
	t.Helper()
	var sb strings.Builder
	outputs := make(chan CommandOutput, 64)
	task, err := NewService().StartCommand(context.Background(), req, func(o CommandOutput) { outputs <- o })
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	var result CommandResult
	select {
	case result = <-task.Result:
	case <-time.After(20 * time.Second):
		t.Fatal("command did not finish")
	}
	close(outputs)
	for o := range outputs {
		sb.WriteString(o.Data)
	}
	return result, sb.String()
}

func TestCmdExePassesQuotesVerbatim(t *testing.T) {
	result, out := runForTest(t, CommandRequest{RequestID: "q", Shell: ShellCMD, Command: `if "a b"=="a b" echo "quoted ok"`, TimeoutSec: 10})
	if result.Status != "completed" || result.ExitCode != 0 {
		t.Fatalf("unexpected result %+v output %q", result, out)
	}
	if !strings.Contains(out, `"quoted ok"`) {
		t.Fatalf("expected verbatim quoted output, got %q", out)
	}
}

func TestCmdExeTimeoutKillsProcessTree(t *testing.T) {
	start := time.Now()
	// The grandchild ping holds the output pipe; killing only cmd.exe would
	// leave the command hanging until ping finishes (~30s).
	result, _ := runForTest(t, CommandRequest{RequestID: "t", Shell: ShellCMD, Command: `ping -n 30 127.0.0.1 | findstr Reply`, TimeoutSec: 1})
	if result.Status != "timeout" {
		t.Fatalf("expected timeout, got %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took too long (%s); process tree not killed", elapsed)
	}
}
