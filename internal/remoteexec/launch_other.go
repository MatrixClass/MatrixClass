//go:build !windows && !linux && !darwin

package remoteexec

import (
	"context"
	"time"
)

func launchApplication(context.Context, AppLaunchRequest, string) CommandResult {
	now := time.Now()
	return CommandResult{Status: "failed", Error: "remote application launch is only supported on Windows", ExitCode: -1, StartedAt: now, FinishedAt: now}
}
