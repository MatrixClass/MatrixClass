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

type linuxProcessInspector struct{ procRoot string }

func newPlatformProcessInspector() ProcessInspector { return &linuxProcessInspector{procRoot: "/proc"} }

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
	if pid == 0 || pid == uint32(os.Getpid()) {
		return errors.New("control: refusing to terminate protected process")
	}
	if err := syscall.Kill(int(pid), syscall.SIGTERM); err != nil {
		return fmt.Errorf("control: terminate linux process %d: %w", pid, err)
	}
	return nil
}
