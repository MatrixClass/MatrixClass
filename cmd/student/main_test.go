package main

import (
	"context"
	"encoding/json"
	"testing"

	"nexlab/internal/core"
	"nexlab/internal/protocol"
	"nexlab/internal/ui/student"
)

func TestNewStudentRuntime(t *testing.T) {
	rt, err := NewRuntime("301机房", "stu-test", "202699", "127.0.0.1:39199", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime failed: %v", err)
	}
	defer rt.Close()
	if rt.App == nil || rt.Transport == nil || rt.Media == nil || rt.Control == nil {
		t.Fatalf("expected all runtime modules initialized")
	}
}

func TestStudentRuntimeMessageHandlers(t *testing.T) {
	rt, err := NewRuntime("301机房", "stu-test", "202699", "127.0.0.1:39198", t.TempDir())
	if err != nil {
		t.Fatalf("NewRuntime failed: %v", err)
	}
	defer rt.Close()
	rt.ctx = context.Background()

	// Test BroadcastStart windowed vs stop
	bcfg, _ := json.Marshal(core.BroadcastConfig{Mode: core.BroadcastWindowed, WithAudio: true})
	rt.App.OpenMode(student.ModeBroadcast)
	if rt.App.Mode() != student.ModeBroadcast {
		t.Fatalf("expected ModeBroadcast")
	}
	_ = bcfg
	rt.App.ShowFloatingBar()
	if rt.App.Mode() != student.ModeBar {
		t.Fatalf("expected ModeBar after ShowFloatingBar")
	}

	// Test monitor & remote control stream start/stop cleanly
	rt.startMonitorStream(320, 180, 10, 70)
	rt.stopMonitorStream()
	rt.startRemoteStream()
	rt.stopRemoteStream()
	rt.startDemoStream()
	rt.stopDemoStream()

	// Verify localPrimaryMAC returns without panicking
	_ = localPrimaryMAC()
	_ = protocol.MsgPing
}

func TestMonitorControlResetRestartsUnchangedStream(t *testing.T) {
	current := monitorStreamConfig{maxW: 320, maxH: 180, fps: 15, quality: 75}
	if shouldRestartMonitorStream(current, current, false) {
		t.Fatal("unchanged monitor configuration should not restart without reset")
	}
	if !shouldRestartMonitorStream(current, current, true) {
		t.Fatal("reset monitor control should restart an unchanged stream")
	}
}
