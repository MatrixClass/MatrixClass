//go:build linux

package remoteexec

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

func executeCommand(ctx context.Context, req CommandRequest, maxOutput int, onOutput func(CommandOutput)) CommandResult {
	started := time.Now()
	result := CommandResult{StartedAt: started, Status: "failed", ExitCode: -1}
	name, args := "/bin/sh", []string{"-c", req.Command}
	switch req.Shell {
	case ShellSH:
	case ShellBash:
		name, args = "/bin/bash", []string{"-lc", req.Command}
	case ShellPowerShell:
		name, args = "pwsh", []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", req.Command}
	default:
		result.Error = ErrInvalidShell.Error()
		result.FinishedAt = time.Now()
		return result
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		result.Error = err.Error()
		result.FinishedAt = time.Now()
		return result
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		result.Error = err.Error()
		result.FinishedAt = time.Now()
		return result
	}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			result.Status, result.Error = commandContextStatus(ctx)
		} else {
			result.Error = err.Error()
		}
		result.FinishedAt = time.Now()
		return result
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go readLinuxOutput(stdout, "stdout", req.RequestID, maxOutput, onOutput, &wg)
	go readLinuxOutput(stderr, "stderr", req.RequestID, maxOutput, onOutput, &wg)
	waitErr := cmd.Wait()
	wg.Wait()
	result.FinishedAt = time.Now()
	if ctx.Err() != nil {
		result.Status, result.Error = commandContextStatus(ctx)
		return result
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Error = waitErr.Error()
		}
		return result
	}
	result.ExitCode, result.Status = 0, "completed"
	return result
}

func commandContextStatus(ctx context.Context) (string, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout", ctx.Err().Error()
	}
	return "cancelled", context.Canceled.Error()
}

func readLinuxOutput(r io.Reader, stream, requestID string, maxOutput int, onOutput func(CommandOutput), wg *sync.WaitGroup) {
	defer wg.Done()
	reader := bufio.NewReader(r)
	total := 0
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 && total < maxOutput {
			remaining := maxOutput - total
			if len(line) > remaining {
				line = line[:remaining]
			}
			total += len(line)
			if onOutput != nil {
				onOutput(CommandOutput{RequestID: requestID, Stream: stream, Data: line})
			}
		}
		if err != nil {
			return
		}
	}
}
