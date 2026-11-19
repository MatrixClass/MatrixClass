//go:build linux

package control

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var ErrPowerUnsupportedPlatform = errors.New("control: Linux power action is unavailable")

var linuxPowerLookPath = exec.LookPath

type linuxPowerExecutor struct {
	command func(context.Context, string, ...string) error
}

func newPlatformPowerExecutor() PowerExecutor {
	return &linuxPowerExecutor{command: runLinuxPowerCommand}
}

func (e *linuxPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	return e.runLinuxAction("shutdown", timeoutSec, reason)
}

func (e *linuxPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	return e.runLinuxAction("reboot", timeoutSec, reason)
}

func (e *linuxPowerExecutor) Logoff() error {
	user := currentLinuxUser()
	if user == "" {
		return fmt.Errorf("%w: current user unavailable", ErrPowerUnsupportedPlatform)
	}
	if _, err := linuxPowerLookPath("loginctl"); err == nil {
		return e.command(context.Background(), "loginctl", "terminate-user", user)
	}
	if _, err := linuxPowerLookPath("pkill"); err == nil {
		return e.command(context.Background(), "pkill", "-TERM", "-u", user)
	}
	return fmt.Errorf("%w: no logoff command", ErrPowerUnsupportedPlatform)
}

func (e *linuxPowerExecutor) runLinuxAction(action string, timeoutSec uint32, reason string) error {
	if name, args, ok := linuxPowerCommand(action, timeoutSec, reason); ok {
		return e.command(context.Background(), name, args...)
	}
	return fmt.Errorf("%w: no command for %s", ErrPowerUnsupportedPlatform, action)
}

func linuxPowerCommand(action string, timeoutSec uint32, reason string) (string, []string, bool) {
	if path, err := linuxPowerLookPath("systemctl"); err == nil {
		args := []string{action}
		if reason != "" {
			args = append(args, "--message", reason)
		}
		return path, args, true
	}
	if action == "shutdown" {
		delay := "+0"
		if timeoutSec > 0 {
			delay = "+" + strconv.FormatUint(uint64((timeoutSec+59)/60), 10)
		}
		if path, err := linuxPowerLookPath("shutdown"); err == nil {
			args := []string{"-h", delay}
			if reason != "" {
				args = append(args, reason)
			}
			return path, args, true
		}
		return "", nil, false
	}
	if path, err := linuxPowerLookPath("reboot"); err == nil {
		return path, []string{}, true
	}
	return "", nil, false
}

func runLinuxPowerCommand(parent context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
		}
		return err
	}
	return nil
}

func currentLinuxUser() string {
	output, err := exec.Command("id", "-un").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
