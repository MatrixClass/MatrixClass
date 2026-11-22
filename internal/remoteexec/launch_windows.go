//go:build windows

package remoteexec

import (
	"context"
	"os/exec"
	"time"
)

func launchApplication(ctx context.Context, req AppLaunchRequest, executable string) CommandResult {
	started := time.Now()
	cmd := exec.CommandContext(ctx, executable, splitCommandLine(req.Arguments, false)...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if err := cmd.Start(); err != nil {
		return CommandResult{Status: "failed", Error: err.Error(), ExitCode: -1, StartedAt: started, FinishedAt: time.Now()}
	}
	if !req.Wait {
		return CommandResult{Status: "completed", ExitCode: 0, StartedAt: started, FinishedAt: time.Now()}
	}
	err := cmd.Wait()
	result := CommandResult{Status: "completed", ExitCode: 0, StartedAt: started, FinishedAt: time.Now()}
	if err != nil {
		result.Status, result.Error = "failed", err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
	}
	return result
}
