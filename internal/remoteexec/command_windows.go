//go:build windows

package remoteexec

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

func executeCommand(ctx context.Context, req CommandRequest, maxOutput int, onOutput func(CommandOutput)) CommandResult {
	started := time.Now()
	result := CommandResult{StartedAt: started, Status: "failed", ExitCode: -1}
	var cmd *exec.Cmd
	if req.Shell == ShellCMD {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", req.Command)
	} else if req.Shell == ShellPowerShell {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", req.Command)
	} else {
		result.Error = ErrInvalidShell.Error()
		result.FinishedAt = time.Now()
		return result
	}
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
		if ctx.Err() == context.Canceled || strings.Contains(strings.ToLower(err.Error()), "canceled") {
			result.Status, result.Error = "cancelled", context.Canceled.Error()
		} else if ctx.Err() == context.DeadlineExceeded {
			result.Status, result.Error = "timeout", context.DeadlineExceeded.Error()
		} else {
			result.Error = err.Error()
		}
		result.FinishedAt = time.Now()
		return result
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go readOutput(stdout, "stdout", req.RequestID, maxOutput, onOutput, &wg)
	go readOutput(stderr, "stderr", req.RequestID, maxOutput, onOutput, &wg)
	waitErr := cmd.Wait()
	wg.Wait()
	result.FinishedAt = time.Now()
	if ctx.Err() == context.DeadlineExceeded {
		result.Status, result.Error = "timeout", ctx.Err().Error()
		return result
	}
	if waitErr != nil && strings.Contains(strings.ToLower(waitErr.Error()), "canceled") {
		result.Status, result.Error = "cancelled", context.Canceled.Error()
		return result
	}
	if ctx.Err() == context.Canceled || errors.Is(waitErr, context.Canceled) || strings.Contains(strings.ToLower(errorString(waitErr)), "context canceled") {
		result.Status, result.Error = "cancelled", ctx.Err().Error()
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

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func readOutput(r io.Reader, stream, requestID string, maxOutput int, onOutput func(CommandOutput), wg *sync.WaitGroup) {
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
