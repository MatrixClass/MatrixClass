//go:build windows

package remoteexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const createNoWindow = 0x08000000

func executeCommand(ctx context.Context, req CommandRequest, maxOutput int, onOutput func(CommandOutput)) CommandResult {
	started := time.Now()
	var cmd *exec.Cmd
	switch req.Shell {
	case ShellCMD:
		// Pass the raw command line: Go's MSVC-style argument escaping
		// (backslash-escaped quotes) is not understood by cmd.exe. With /s,
		// cmd strips exactly the outer pair of quotes and runs the rest verbatim.
		cmd = exec.CommandContext(ctx, "cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: cmdExeCommandLine(req.Command)}
	case ShellPowerShell:
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", req.Command)
	default:
		now := time.Now()
		return CommandResult{StartedAt: started, FinishedAt: now, Status: "failed", ExitCode: -1, Error: ErrInvalidShell.Error()}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killProcessTree(cmd.Process.Pid)
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return nil
	}
	return runPreparedCommand(ctx, cmd, req, maxOutput, onOutput, started)
}

func cmdExeCommandLine(command string) string {
	return `cmd.exe /d /s /c "` + command + `"`
}

// killProcessTree terminates pid and all of its descendants using taskkill.
// It is best effort; the caller still kills the root process directly.
func killProcessTree(pid int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	kill := exec.CommandContext(ctx, "taskkill.exe", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	_ = kill.Run()
}
