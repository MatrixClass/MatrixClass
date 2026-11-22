//go:build darwin

package remoteexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// darwinShellCommand maps a shell to its interpreter. zsh and bash run as
// login shells so /etc/zprofile (path_helper) and the user's profile provide
// the PATH a Terminal user would see (e.g. /opt/homebrew/bin).
func darwinShellCommand(shell Shell, command string) (string, []string, bool) {
	switch shell {
	case ShellZsh:
		return "/bin/zsh", []string{"-lc", command}, true
	case ShellBash:
		return "/bin/bash", []string{"-lc", command}, true
	case ShellSH:
		return "/bin/sh", []string{"-c", command}, true
	default:
		return "", nil, false
	}
}

func executeCommand(ctx context.Context, req CommandRequest, maxOutput int, onOutput func(CommandOutput)) CommandResult {
	started := time.Now()
	name, args, ok := darwinShellCommand(req.Shell, req.Command)
	if !ok {
		return CommandResult{StartedAt: started, FinishedAt: time.Now(), Status: "failed", ExitCode: -1, Error: ErrInvalidShell.Error()}
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
