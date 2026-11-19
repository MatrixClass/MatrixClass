//go:build linux

package control

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var (
	ErrLinuxProcessUnavailable = errors.New("control: Linux process is unavailable")
	ErrLinuxProcessZombie      = errors.New("control: refusing to terminate zombie Linux process")
	ErrLinuxProcessProtected   = errors.New("control: refusing to terminate protected Linux process")
)

type linuxProcessInspector struct {
	procRoot string
	selfPID  uint32
}

func newPlatformProcessInspector() ProcessInspector {
	return &linuxProcessInspector{procRoot: "/proc", selfPID: uint32(os.Getpid())}
}

func (p *linuxProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	entries, err := os.ReadDir(p.procRoot)
	if err != nil {
		return nil, err
	}
	processes := make([]ProcessInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid64, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || pid64 == 0 {
			continue
		}
		name := readLinuxProcessName(filepath.Join(p.procRoot, entry.Name()))
		if name != "" {
			processes = append(processes, ProcessInfo{PID: uint32(pid64), Name: name})
		}
	}
	return processes, nil
}

func readLinuxProcessName(procDir string) string {
	if data, err := os.ReadFile(filepath.Join(procDir, "comm")); err == nil {
		if name := strings.TrimSpace(string(data)); name != "" {
			return name
		}
	}
	data, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
	if value == "" {
		return ""
	}
	return filepath.Base(strings.Fields(value)[0])
}

func (p *linuxProcessInspector) TerminateProcess(pid uint32) error {
	if pid == 0 || pid == uint32(os.Getpid()) || (p.selfPID != 0 && pid == p.selfPID) {
		return ErrLinuxProcessProtected
	}
	procDir := filepath.Join(p.procRoot, strconv.FormatUint(uint64(pid), 10))
	stat, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %d", ErrLinuxProcessUnavailable, pid)
		}
		return fmt.Errorf("control: inspect Linux process %d: %w", pid, err)
	}
	if linuxProcessState(string(stat)) == 'Z' {
		return fmt.Errorf("%w: %d", ErrLinuxProcessZombie, pid)
	}
	if err := syscall.Kill(int(pid), syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("%w: %d", ErrLinuxProcessUnavailable, pid)
		}
		return fmt.Errorf("control: terminate linux process %d: %w", pid, err)
	}
	return nil
}

func linuxProcessState(stat string) rune {
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || closeParen+2 >= len(stat) {
		return 0
	}
	return rune(stat[closeParen+2])
}
