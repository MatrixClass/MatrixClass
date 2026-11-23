//go:build !windows && !linux && !darwin

package remoteexec

import (
	"context"
	"time"
)

func executeCommand(context.Context, CommandRequest, int, func(CommandOutput)) CommandResult {
	now := time.Now()
	return CommandResult{Status: "failed", Error: "remote command execution is unsupported on this platform", StartedAt: now, FinishedAt: now, ExitCode: -1}
}
