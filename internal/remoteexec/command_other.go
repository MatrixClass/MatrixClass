//go:build !windows && !linux

package remoteexec

import (
	"context"
	"time"
)

func executeCommand(context.Context, CommandRequest, int, func(CommandOutput)) CommandResult {
	now := time.Now()
	return CommandResult{Status: "failed", Error: "remote command execution is only supported on Windows", StartedAt: now, FinishedAt: now, ExitCode: -1}
}
