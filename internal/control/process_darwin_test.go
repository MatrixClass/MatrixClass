//go:build darwin

package control

import (
	"context"
	"errors"
	"testing"
)

func TestDarwinProcessInspectorParsesPSOutput(t *testing.T) {
	inspector := &darwinProcessInspector{
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("101|WindowServer\n202|/Applications/Demo.app/Contents/MacOS/Demo\n"), nil
		},
	}
	processes, err := inspector.ListProcesses()
	if err != nil {
		t.Fatalf("ListProcesses failed: %v", err)
	}
	if len(processes) != 2 || processes[0].PID != 101 || processes[0].Name != "WindowServer" || processes[1].Name != "Demo" {
		t.Fatalf("unexpected processes: %+v", processes)
	}
}

func TestDarwinProcessInspectorRejectsSelfAndZero(t *testing.T) {
	inspector := &darwinProcessInspector{selfPID: 42}
	if err := inspector.TerminateProcess(0); !errors.Is(err, ErrDarwinProcessProtected) {
		t.Fatalf("zero pid error = %v, want ErrDarwinProcessProtected", err)
	}
	if err := inspector.TerminateProcess(42); !errors.Is(err, ErrDarwinProcessProtected) {
		t.Fatalf("self pid error = %v, want ErrDarwinProcessProtected", err)
	}
}

func TestDarwinProcessInspectorTerminatesWithKill(t *testing.T) {
	var name string
	var args []string
	inspector := &darwinProcessInspector{
		selfPID: 99,
		command: func(_ context.Context, gotName string, gotArgs ...string) ([]byte, error) {
			name = gotName
			args = append([]string(nil), gotArgs...)
			return nil, nil
		},
	}
	if err := inspector.TerminateProcess(123); err != nil {
		t.Fatalf("TerminateProcess failed: %v", err)
	}
	if name != "kill" || len(args) != 2 || args[0] != "-TERM" || args[1] != "123" {
		t.Fatalf("unexpected kill command: %q %v", name, args)
	}
}
