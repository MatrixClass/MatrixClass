//go:build windows || linux || darwin

package remoteexec

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

const (
	// commandWaitDelay bounds how long Wait keeps draining output after the
	// process exits (or is killed) when descendants still hold the pipes.
	commandWaitDelay = 2 * time.Second
	maxPendingLine   = 64 * 1024
)

// outputWriter is used as exec.Cmd Stdout/Stderr. exec.Cmd copies the pipe
// into it and Wait waits for that copy to finish (bounded by WaitDelay), so
// readers never race with Wait. Output is split into lines and capped at max
// bytes per stream; excess output is drained and discarded.
type outputWriter struct {
	mu        sync.Mutex
	stream    string
	requestID string
	max       int
	total     int
	onOutput  func(CommandOutput)
	pending   []byte
}

func newOutputWriter(stream, requestID string, max int, onOutput func(CommandOutput)) *outputWriter {
	return &outputWriter{stream: stream, requestID: requestID, max: max, onOutput: onOutput}
}

func (w *outputWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, p...)
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		w.emitLocked(w.pending[:idx+1])
		w.pending = w.pending[idx+1:]
	}
	if len(w.pending) >= maxPendingLine {
		w.emitLocked(w.pending)
		w.pending = nil
	}
	if len(w.pending) == 0 {
		w.pending = nil
	}
	return len(p), nil
}

// Flush emits any trailing partial line.
func (w *outputWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.emitLocked(w.pending)
		w.pending = nil
	}
}

func (w *outputWriter) emitLocked(line []byte) {
	if len(line) == 0 || w.total >= w.max {
		return
	}
	if remaining := w.max - w.total; len(line) > remaining {
		line = line[:remaining]
	}
	w.total += len(line)
	if w.onOutput != nil {
		w.onOutput(CommandOutput{RequestID: w.requestID, Stream: w.stream, Data: string(line)})
	}
}

func commandContextStatus(ctx context.Context) (string, string) {
	if ctx.Err() == context.DeadlineExceeded {
		return "timeout", ctx.Err().Error()
	}
	return "cancelled", context.Canceled.Error()
}

// runPreparedCommand starts cmd (already configured with Cancel/SysProcAttr),
// streams its output and maps the outcome to a CommandResult.
func runPreparedCommand(ctx context.Context, cmd *exec.Cmd, req CommandRequest, maxOutput int, onOutput func(CommandOutput), started time.Time) CommandResult {
	result := CommandResult{StartedAt: started, Status: "failed", ExitCode: -1}
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	stdout := newOutputWriter("stdout", req.RequestID, maxOutput, onOutput)
	stderr := newOutputWriter("stderr", req.RequestID, maxOutput, onOutput)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = commandWaitDelay
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			result.Status, result.Error = commandContextStatus(ctx)
		} else {
			result.Error = err.Error()
		}
		result.FinishedAt = time.Now()
		return result
	}
	waitErr := cmd.Wait()
	stdout.Flush()
	stderr.Flush()
	result.FinishedAt = time.Now()
	if ctx.Err() != nil {
		result.Status, result.Error = commandContextStatus(ctx)
		return result
	}
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		// Process exited successfully; only orphaned descendants kept the pipes open.
		waitErr = nil
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
