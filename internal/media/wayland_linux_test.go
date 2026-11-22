//go:build linux

package media

import (
	"errors"
	"testing"
)

func TestWaylandCaptureReportsUnavailableInsteadOfSyntheticFrame(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	// No session bus: authorization cannot succeed, and must not fabricate.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/matrixclass-test-bus")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	capturer := NewScreenCapturer(CaptureOptions{Width: 16, Height: 16})
	defer capturer.Close()
	if capturer.Backend() != CaptureBackendWayland {
		t.Fatalf("expected Wayland backend marker, got %q", capturer.Backend())
	}
	if frame, err := capturer.CaptureScreen(); err == nil || frame != nil || !errors.Is(err, ErrScreenCaptureUnavailable) {
		t.Fatalf("expected ErrScreenCaptureUnavailable without a frame, got frame=%v err=%v", frame != nil, err)
	}
}

func TestWaylandPortalCaptureWrapsErrorsAndClosesIdempotently(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/matrixclass-test-bus")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	w := newWaylandPortalCapture(CaptureOptions{})
	if w.Backend() != CaptureBackendWayland {
		t.Fatalf("unexpected backend %q", w.Backend())
	}
	_, _, err := w.Capture(0, 0, false)
	if !errors.Is(err, ErrScreenCaptureUnavailable) || !errors.Is(err, ErrWaylandAuthorizationPending) {
		t.Fatalf("expected pending authorization wrapped in ErrScreenCaptureUnavailable, got %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Capture(0, 0, false); !errors.Is(err, ErrWaylandCaptureClosed) {
		t.Fatalf("expected closed error, got %v", err)
	}
	if st := w.WaylandCaptureStatus(); st.State != WaylandCaptureStopped {
		t.Fatalf("expected stopped state after Close, got %+v", st)
	}
}

func TestPipeWireArchSupport(t *testing.T) {
	// Every target this project ships on Linux (amd64, arm64, loong64) is
	// supported; anything else must report a clear unavailable error.
	if pwArchSupported() {
		return
	}
	if err := pwAvailable(); !errors.Is(err, ErrWaylandPipeWireUnavailable) {
		t.Fatalf("expected ErrWaylandPipeWireUnavailable on unsupported arch, got %v", err)
	}
}
