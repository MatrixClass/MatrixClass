//go:build linux

package media

import (
	"errors"
	"testing"
)

func TestWaylandCaptureReportsUnavailableInsteadOfSyntheticFrame(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")

	capturer := NewScreenCapturer(CaptureOptions{Width: 16, Height: 16})
	defer capturer.Close()
	if capturer.Backend() != CaptureBackendWayland {
		t.Fatalf("expected Wayland backend marker, got %q", capturer.Backend())
	}
	if _, err := capturer.CaptureScreen(); err == nil || !errors.Is(err, ErrScreenCaptureUnavailable) {
		t.Fatalf("expected ErrScreenCaptureUnavailable, got %v", err)
	}
}

func TestWaylandCaptureAuthorizationAndDisconnectStates(t *testing.T) {
	capture := &waylandCapture{}
	if err := capture.StartWaylandCapture(); !errors.Is(err, ErrWaylandPortalUnavailable) {
		t.Fatalf("expected unavailable portal, got %v", err)
	}
	if got := capture.WaylandCaptureStatus().State; got != WaylandCaptureAuthorizing {
		t.Fatalf("expected authorizing state, got %q", got)
	}
	if err := capture.AuthorizeWaylandCapture(false); !errors.Is(err, ErrWaylandPortalCancelled) {
		t.Fatalf("expected authorization cancellation, got %v", err)
	}
	status := capture.WaylandCaptureStatus()
	if status.State != WaylandCaptureStopped || !errors.Is(status.LastError, ErrWaylandPortalCancelled) {
		t.Fatalf("unexpected cancelled status: %+v", status)
	}

	if err := capture.AuthorizeWaylandCapture(true); !errors.Is(err, ErrWaylandPipeWireDisconnected) {
		t.Fatalf("expected missing PipeWire bridge, got %v", err)
	}
	if err := capture.MarkWaylandPipeWireDisconnected(); !errors.Is(err, ErrWaylandPipeWireDisconnected) {
		t.Fatalf("expected PipeWire disconnect, got %v", err)
	}
	if err := capture.ReconnectWaylandCapture(); !errors.Is(err, ErrWaylandPipeWireDisconnected) {
		t.Fatalf("expected reconnect to remain unavailable, got %v", err)
	}
	status = capture.WaylandCaptureStatus()
	if status.State != WaylandCaptureReconnecting || status.Reconnects != 1 || !status.Authorized || status.Connected {
		t.Fatalf("unexpected reconnect status: %+v", status)
	}
}
