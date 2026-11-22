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
	command := p.command
	if command == nil {
		command = runDarwinOutput
	}
	output, err := command(context.Background(), "ps", "-axo", "pid=,ppid=,uid=,comm=")
	if err != nil {
		return nil, fmt.Errorf("control: list macOS processes: %w", err)
	}
	euid := uint32(os.Geteuid())
	type candidate struct {
		pid  uint32
		name string
	}
	var candidates []candidate
	parents := make(map[uint32]uint32)
	for _, line := range strings.Split(string(output), "\n") {
		pid, ppid, uid, path, ok := parseDarwinPSLine(line)
		if !ok {
			continue
		}
		parents[pid] = ppid
		if uid != euid {
			continue
		}
		name := filepath.Base(path)
		if name == "" || name == "." || name == "/" {
			continue
		}
		candidates = append(candidates, candidate{pid: pid, name: strings.TrimSuffix(name, ".app")})
	}
	self := p.selfPID
	if self == 0 {
		self = uint32(os.Getpid())
	}
	lineage := processLineage(self, parents)
	var processes []ProcessInfo
	for _, c := range candidates {
		if lineage[c.pid] {
			continue
		}
		processes = append(processes, ProcessInfo{PID: c.pid, Name: c.name})
	}
	return processes, nil
}

// parseDarwinPSLine parses "pid ppid uid command-path" (the path may contain spaces).
func parseDarwinPSLine(line string) (pid, ppid, uid uint32, path string, ok bool) {
	rest := strings.TrimSpace(line)
	var nums [3]uint32
	for i := range nums {
		idx := strings.IndexAny(rest, " \t")
		if idx < 0 {
			return 0, 0, 0, "", false
		}
		v, err := strconv.ParseUint(rest[:idx], 10, 32)
		if err != nil {
			return 0, 0, 0, "", false
		}
		nums[i] = uint32(v)
		rest = strings.TrimSpace(rest[idx:])
	}
	if nums[0] == 0 || rest == "" {
		return 0, 0, 0, "", false
	}
	return nums[0], nums[1], nums[2], rest, true
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

// platformProtectedProcesses are macOS session/system processes that policy
// enforcement (especially whitelist mode) must never terminate.
var platformProtectedProcesses = []string{
	"kernel_task",
	"launchd",
	"loginwindow",
	"WindowServer",
	"Dock",
	"Finder",
	"SystemUIServer",
	"ControlCenter",
	"NotificationCenter",
	"Spotlight",
	"coreaudiod",
	"cfprefsd",
	"distnoted",
	"UserEventAgent",
	"TextInputMenuAgent",
	"universalaccessd",
	"WindowManager",
	"talagent",
	"pboard",
	"secd",
	"trustd",
	"securityd",
	"opendirectoryd",
	"com.apple.*",
}
