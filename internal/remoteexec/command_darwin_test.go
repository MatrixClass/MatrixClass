//go:build darwin

package remoteexec

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDarwinShellCommandMapping(t *testing.T) {
	cases := map[Shell][]string{
		ShellZsh:  {"/bin/zsh", "-lc", "echo hi"},
		ShellBash: {"/bin/bash", "-lc", "echo hi"},
		ShellSH:   {"/bin/sh", "-c", "echo hi"},
	}
	for shell, want := range cases {
		name, args, ok := darwinShellCommand(shell, "echo hi")
		if !ok || !reflect.DeepEqual(append([]string{name}, args...), want) {
			t.Fatalf("%s: got %q %q %v", shell, name, args, ok)
		}
	}
	if _, _, ok := darwinShellCommand(ShellCMD, "dir"); ok {
		t.Fatal("cmd must not be accepted on macOS")
	}
	if DefaultShell() != ShellZsh || !supportedShell(ShellZsh) || supportedShell(ShellPowerShell) {
		t.Fatal("unexpected macOS shell support")
	}
}

func TestDarwinZshCommandReportsExitCodeAndOutput(t *testing.T) {
	var out strings.Builder
	outputs := make(chan CommandOutput, 8)
	task, err := NewService().StartCommand(context.Background(), CommandRequest{RequestID: "z", Shell: ShellZsh, Command: "echo hello; echo oops >&2; exit 3", TimeoutSec: 10}, func(o CommandOutput) { outputs <- o })
	if err != nil {
		t.Fatalf("StartCommand: %v", err)
	}
	result := <-task.Result
	close(outputs)
	for o := range outputs {
		out.WriteString(o.Stream + ":" + o.Data)
	}
	if result.ExitCode != 3 || result.Status != "failed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.Contains(out.String(), "stdout:hello") || !strings.Contains(out.String(), "stderr:oops") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

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

func TestDarwinDiscoverApplicationsFindsSystemApps(t *testing.T) {
	apps := discoverApplications()
	for _, app := range apps {
		if app.Name == "" || app.Executable == "" || app.ID == "" {
			t.Fatalf("incomplete app: %+v", app)
		}
	}
	found := false
	for _, app := range apps {
		if strings.HasSuffix(app.Executable, "/TextEdit.app/Contents/MacOS/TextEdit") || strings.Contains(app.Executable, "/Safari.app/") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected TextEdit or Safari among %d discovered apps", len(apps))
	}
}
