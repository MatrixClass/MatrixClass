//go:build linux

package remoteexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func executeCommand(ctx context.Context, req CommandRequest, maxOutput int, onOutput func(CommandOutput)) CommandResult {
	started := time.Now()
	name, args := "/bin/sh", []string{"-c", req.Command}
	switch req.Shell {
	case ShellSH:
	case ShellBash:
		name, args = "/bin/bash", []string{"-lc", req.Command}
	case ShellPowerShell:
		name, args = "pwsh", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", req.Command}
	default:
		now := time.Now()
		return CommandResult{StartedAt: started, FinishedAt: now, Status: "failed", ExitCode: -1, Error: ErrInvalidShell.Error()}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	// Run the shell in its own process group so cancellation/timeout can kill
	// every descendant, not only the shell itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	return runPreparedCommand(ctx, cmd, req, maxOutput, onOutput, started)
}
