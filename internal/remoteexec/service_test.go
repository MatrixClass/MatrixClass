package remoteexec

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestStartCommandCapturesOutput(t *testing.T) {
	service := NewService()
	outputs := make(chan CommandOutput, 4)
	task, err := service.StartCommand(context.Background(), CommandRequest{RequestID: "req-1", Shell: ShellCMD, Command: "echo hello", TimeoutSec: 5}, func(output CommandOutput) {
		outputs <- output
	})
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}
	result := <-task.Result
	if result.Status != "completed" || result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	var joined string
	for len(outputs) > 0 {
		joined += (<-outputs).Data
	}
	if !strings.Contains(strings.ToLower(joined), "hello") {
		t.Fatalf("expected output to contain hello, got %q", joined)
	}
}

func TestStartCommandCanBeCancelled(t *testing.T) {
	service := NewService()
	task, err := service.StartCommand(context.Background(), CommandRequest{RequestID: "req-2", Shell: ShellCMD, Command: "ping 127.0.0.1 -n 20", TimeoutSec: 60}, nil)
	if err != nil {
		t.Fatalf("StartCommand failed: %v", err)
	}
	task.Cancel()
	select {
	case result := <-task.Result:
		t.Logf("cancel result: %+v", result)
		if result.Status != "cancelled" {
			t.Fatalf("expected cancelled result, got %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancelled command")
	}
}

func TestStartCommandRejectsUnsupportedShell(t *testing.T) {
	_, err := NewService().StartCommand(context.Background(), CommandRequest{Shell: "bash", Command: "echo nope"}, nil)
	if err != ErrInvalidShell {
		t.Fatalf("expected ErrInvalidShell, got %v", err)
	}
}

func TestLaunchAppRejectsMissingExecutable(t *testing.T) {
	result := NewService().LaunchApp(context.Background(), AppLaunchRequest{RequestID: "req-3", Executable: `C:\does-not-exist\missing.exe`}, nil)
	if result.Status != "failed" || result.Error == "" {
		t.Fatalf("expected missing executable failure, got %+v", result)
	}
}
