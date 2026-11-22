package control

import (
	"testing"

	"nexlab/internal/core"
)

func TestProcessLineageCoversAncestorsAndDescendants(t *testing.T) {
	// 1 -> 10 (session) -> 20 (launcher) -> 30 (self) -> 40 (child) -> 50 (grandchild)
	// 1 -> 60 (unrelated) -> 70
	parents := map[uint32]uint32{1: 0, 10: 1, 20: 10, 30: 20, 40: 30, 50: 40, 60: 1, 70: 60}
	got := processLineage(30, parents)
	for _, pid := range []uint32{1, 10, 20, 30, 40, 50} {
		if !got[pid] {
			t.Fatalf("pid %d should be protected lineage: %v", pid, got)
		}
	}
	for _, pid := range []uint32{60, 70} {
		if got[pid] {
			t.Fatalf("pid %d should not be in lineage", pid)
		}
	}
	parentOf := func(pid uint32) (uint32, bool) { p, ok := parents[pid]; return p, ok }
	for pid, want := range map[uint32]bool{1: true, 20: true, 30: true, 50: true, 60: false, 70: false} {
		if isAncestorOrDescendant(30, pid, parentOf) != want {
			t.Fatalf("isAncestorOrDescendant(30, %d) != %v", pid, want)
		}
	}
	// Cycles must terminate.
	_ = processLineage(5, map[uint32]uint32{5: 6, 6: 5, 7: 8, 8: 7})
}

type staticInspector struct {
	procs      []ProcessInfo
	terminated []uint32
}

func (s *staticInspector) ListProcesses() ([]ProcessInfo, error) { return s.procs, nil }
func (s *staticInspector) TerminateProcess(pid uint32) error {
	s.terminated = append(s.terminated, pid)
	return nil
}

func TestWhitelistSkipsPlatformProtectedProcesses(t *testing.T) {
	var procs []ProcessInfo
	for i, name := range platformProtectedProcesses {
		procs = append(procs, ProcessInfo{PID: uint32(1000 + i), Name: name})
	}
	procs = append(procs, ProcessInfo{PID: 9999, Name: "game.exe"})
	insp := &staticInspector{procs: procs}
	enf := NewPolicyEnforcer(PolicyOptions{Inspector: insp, SelfPID: 1})
	if err := enf.ApplyRule(core.PolicyRule{Mode: PolicyModeWhitelist, AllowedApps: []string{"notepad.exe"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := enf.EnforceProcessesOnce(); err != nil {
		t.Fatal(err)
	}
	if len(insp.terminated) != 1 || insp.terminated[0] != 9999 {
		t.Fatalf("expected only game.exe to be terminated, got %v", insp.terminated)
	}
}
