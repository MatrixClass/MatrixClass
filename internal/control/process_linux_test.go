//go:build linux

package control

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxProcessInspectorReadsProcEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "101"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "101", "comm"), []byte("demo-app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "not-a-pid"), 0755); err != nil {
		t.Fatal(err)
	}
	processes, err := (&linuxProcessInspector{procRoot: root}).ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses failed: %v", err)
	}
	if len(processes) != 1 || processes[0].PID != 101 || processes[0].Name != "demo-app" {
		t.Fatalf("unexpected processes: %+v", processes)
	}
}

func TestLinuxProcessInspectorRejectsZombie(t *testing.T) {
	root := t.TempDir()
	procDir := filepath.Join(root, "303")
	if err := os.Mkdir(procDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "comm"), []byte("zombie\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "stat"), []byte("303 (zombie) Z 1 2 3 4 5 6\n"), 0644); err != nil {
		t.Fatal(err)
	}
	inspector := &linuxProcessInspector{procRoot: root, selfPID: 999}
	if err := inspector.TerminateProcess(303); !errors.Is(err, ErrLinuxProcessZombie) {
		t.Fatalf("TerminateProcess error = %v, want ErrLinuxProcessZombie", err)
	}
}

func TestLinuxProcessInspectorRejectsMissingProcess(t *testing.T) {
	inspector := &linuxProcessInspector{procRoot: t.TempDir(), selfPID: 999}
	if err := inspector.TerminateProcess(404); !errors.Is(err, ErrLinuxProcessUnavailable) {
		t.Fatalf("TerminateProcess error = %v, want ErrLinuxProcessUnavailable", err)
	}
}

func TestLinuxProcessInspectorFallsBackToCmdline(t *testing.T) {
	root := t.TempDir()
	procDir := filepath.Join(root, "202")
	if err := os.Mkdir(procDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "cmdline"), []byte("/usr/bin/demo\x00--worker\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	processes, err := (&linuxProcessInspector{procRoot: root}).ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses failed: %v", err)
	}
	if len(processes) != 1 || processes[0].Name != "demo" {
		t.Fatalf("unexpected cmdline process: %+v", processes)
	}
}
