//go:build darwin

package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrDarwinProcessProtected = errors.New("control: refusing to terminate protected macOS process")

type darwinProcessInspector struct {
	selfPID uint32
	command func(context.Context, string, ...string) ([]byte, error)
}

func newPlatformProcessInspector() ProcessInspector {
	return &darwinProcessInspector{selfPID: uint32(os.Getpid()), command: runDarwinOutput}
}

func (p *darwinProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	output, err := p.command(context.Background(), "ps", "-axo", "pid=,command=")
	if err != nil {
		return nil, fmt.Errorf("control: list macOS processes: %w", err)
	}
	var processes []ProcessInfo
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		pid64, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil || pid64 == 0 {
			continue
		}
		name := filepath.Base(fields[1])
		if name == "" || name == "." || name == "/" {
			continue
		}
		processes = append(processes, ProcessInfo{PID: uint32(pid64), Name: strings.TrimSuffix(name, ".app")})
	}
	return processes, nil
}

func (p *darwinProcessInspector) TerminateProcess(pid uint32) error {
	if pid == 0 || pid == p.selfPID || pid == uint32(os.Getpid()) {
		return ErrDarwinProcessProtected
	}
	command := p.command
	if command == nil {
		command = runDarwinOutput
	}
	if _, err := command(context.Background(), "kill", "-TERM", strconv.FormatUint(uint64(pid), 10)); err != nil {
		return fmt.Errorf("control: terminate macOS process %d: %w", pid, err)
	}
	return nil
}

func runDarwinOutput(parent context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(parent, name, args...).CombinedOutput()
}
