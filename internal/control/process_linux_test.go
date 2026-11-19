//go:build linux

package control

import (
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
