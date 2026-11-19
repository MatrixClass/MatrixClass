//go:build darwin

package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

var ErrDarwinPowerUnavailable = errors.New("control: macOS power action is unavailable")

type darwinPowerExecutor struct {
	command func(context.Context, string, ...string) error
}

func newPlatformPowerExecutor() PowerExecutor {
	return &darwinPowerExecutor{command: runDarwinCommand}
}

func (e *darwinPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	args := []string{"-h", darwinPowerDelay(timeoutSec)}
	if reason != "" {
		args = append(args, "-o", reason)
	}
	return e.run("shutdown", args...)
}

func (e *darwinPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	args := []string{"-r", darwinPowerDelay(timeoutSec)}
	if reason != "" {
		args = append(args, "-o", reason)
	}
	return e.run("shutdown", args...)
}

func (e *darwinPowerExecutor) Logoff() error {
	return e.run("osascript", "-e", "tell application \"System Events\" to log out")
}

func (e *darwinPowerExecutor) run(name string, args ...string) error {
	if e.command == nil {
		e.command = runDarwinCommand
	}
	if err := e.command(context.Background(), name, args...); err != nil {
		return fmt.Errorf("%w: %v", ErrDarwinPowerUnavailable, err)
	}
	return nil
}

func darwinPowerDelay(timeoutSec uint32) string {
	if timeoutSec == 0 {
		return "now"
	}
	return "+" + strconv.FormatUint(uint64(timeoutSec), 10)
}
