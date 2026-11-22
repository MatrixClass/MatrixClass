//go:build darwin

package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var ErrDarwinPowerUnavailable = errors.New("control: macOS power action is unavailable")

const (
	darwinShutdownPath  = "/sbin/shutdown"
	darwinOsascriptPath = "/usr/bin/osascript"

	// Apple Events understood by loginwindow (AERegistry.h). These act without
	// the confirmation dialog that `tell application "System Events" to shut
	// down` shows (which a student could simply cancel).
	darwinEventShutDown      = "shut"
	darwinEventRestart       = "rest"
	darwinEventReallyLogOut  = "rlgo"
	darwinPowerCommandTimout = 30 * time.Second
)

type darwinPowerExecutor struct {
	command func(context.Context, string, ...string) error
	// start launches a command without waiting (used for delayed actions).
	start func(string, ...string) error
	euid  func() int
}

func newPlatformPowerExecutor() PowerExecutor {
	return &darwinPowerExecutor{command: runDarwinCommand, start: startDarwinCommand, euid: os.Geteuid}
}

func (e *darwinPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	return e.power("-h", darwinEventShutDown, timeoutSec, reason)
}

func (e *darwinPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	return e.power("-r", darwinEventRestart, timeoutSec, reason)
}

func (e *darwinPowerExecutor) Logoff() error {
	return e.runNow(darwinOsascriptPath, darwinLoginwindowScript(darwinEventReallyLogOut, 0)...)
}

// power uses /sbin/shutdown when running as root (e.g. a LaunchDaemon) and
// otherwise asks loginwindow via Apple Events, which works for the logged-in
// user without administrator rights.
func (e *darwinPowerExecutor) power(flag, event string, timeoutSec uint32, reason string) error {
	euid := os.Geteuid
	if e.euid != nil {
		euid = e.euid
	}
	if euid() == 0 {
		return e.runNow(darwinShutdownPath, darwinShutdownArgs(flag, timeoutSec, reason)...)
	}
	args := darwinLoginwindowScript(event, timeoutSec)
	if timeoutSec == 0 {
		return e.runNow(darwinOsascriptPath, args...)
	}
	start := e.start
	if start == nil {
		start = startDarwinCommand
	}
	if err := start(darwinOsascriptPath, args...); err != nil {
		return fmt.Errorf("%w: %v", ErrDarwinPowerUnavailable, err)
	}
	return nil
}

func (e *darwinPowerExecutor) runNow(name string, args ...string) error {
	command := e.command
	if command == nil {
		command = runDarwinCommand
	}
	ctx, cancel := context.WithTimeout(context.Background(), darwinPowerCommandTimout)
	defer cancel()
	if err := command(ctx, name, args...); err != nil {
		return fmt.Errorf("%w: %v", ErrDarwinPowerUnavailable, darwinExplainAppleEventError(err))
	}
	return nil
}

// darwinShutdownArgs builds shutdown(8) arguments. macOS shutdown takes the
// delay in minutes ("+N") and the warning message as trailing arguments; its
// -o flag means "halt/reboot directly" and takes no value.
func darwinShutdownArgs(flag string, timeoutSec uint32, reason string) []string {
	args := []string{flag, darwinPowerDelay(timeoutSec)}
	if reason = strings.TrimSpace(reason); reason != "" {
		args = append(args, reason)
	}
	return args
}

func darwinPowerDelay(timeoutSec uint32) string {
	if timeoutSec == 0 {
		return "now"
	}
	minutes := (uint64(timeoutSec) + 59) / 60
	return "+" + strconv.FormatUint(minutes, 10)
}

// darwinLoginwindowScript returns osascript arguments sending a raw Apple
// Event to loginwindow, optionally after a delay in seconds.
func darwinLoginwindowScript(event string, delaySec uint32) []string {
	var args []string
	if delaySec > 0 {
		args = append(args, "-e", "delay "+strconv.FormatUint(uint64(delaySec), 10))
	}
	return append(args, "-e", `tell application "loginwindow" to «event aevt`+event+`»`)
}

// darwinExplainAppleEventError adds a hint for the TCC Automation error.
func darwinExplainAppleEventError(err error) error {
	if err != nil && strings.Contains(err.Error(), "-1743") {
		return fmt.Errorf("%w (需要在系统设置>隐私与安全性>自动化中允许本应用控制 loginwindow/System Events)", err)
	}
	return err
}

func startDarwinCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
