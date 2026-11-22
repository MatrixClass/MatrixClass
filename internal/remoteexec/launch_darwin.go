//go:build darwin

package remoteexec

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func launchApplication(ctx context.Context, req AppLaunchRequest, executable string) CommandResult {
	started := time.Now()
	name, args := darwinLaunchCommand(executable, splitCommandLine(req.Arguments, true), req.Wait)
	cmd := exec.CommandContext(ctx, name, args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if err := cmd.Start(); err != nil {
		return CommandResult{Status: "failed", Error: err.Error(), ExitCode: -1, StartedAt: started, FinishedAt: time.Now()}
	}
	if !req.Wait {
		// Reap the child (for `open` this returns as soon as LaunchServices
		// has accepted the request) so no zombie is left behind.
		go func() { _ = cmd.Wait() }()
		return CommandResult{Status: "completed", ExitCode: 0, StartedAt: started, FinishedAt: time.Now()}
	}
	err := cmd.Wait()
	result := CommandResult{Status: "completed", ExitCode: 0, StartedAt: started, FinishedAt: time.Now()}
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result
}
