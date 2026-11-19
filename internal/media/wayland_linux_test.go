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
