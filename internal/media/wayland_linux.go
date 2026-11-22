//go:build linux

package media

import (
	"fmt"
	"os"

	"nexlab/internal/core"
)

// waylandPortalCapture is the Wayland nativeScreenCapture: xdg-desktop-portal
// ScreenCast authorization + PipeWire frames (portal_linux.go,
// pipewire_linux.go), supervised by waylandSupervisor (portal_linux_api.go).
// Authorization starts lazily on the first Capture (or an explicit
// RetryWaylandAuthorization) and never blocks the caller.
type waylandPortalCapture struct {
	sup *waylandSupervisor
}

// newWaylandCapture is the Wayland entry point for newNativeScreenCapture.
func newWaylandCapture(opts CaptureOptions) nativeScreenCapture {
	return newWaylandPortalCapture(opts)
}

func newWaylandPortalCapture(CaptureOptions) *waylandPortalCapture {
	var tokens waylandTokenStore
	if path, err := portalRestoreTokenPath(); err == nil {
		tokens = portalFileTokenStore{path: path}
	}
	allowScreenshot := os.Getenv("MATRIXCLASS_WAYLAND_SCREENSHOT_FALLBACK") != "0"
	return &waylandPortalCapture{sup: newWaylandSupervisor(waylandPortalDriver{}, tokens, waylandDefaultTiming, allowScreenshot)}
}

func (w *waylandPortalCapture) Backend() CaptureBackend { return CaptureBackendWayland }

// Capture returns the newest real frame. While authorization is pending, was
// cancelled/denied, or the stream is reconnecting it returns an error wrapping
// both ErrScreenCaptureUnavailable and the specific ErrWayland* cause.
func (w *waylandPortalCapture) Capture(maxW, maxH int, withNV12 bool) (*core.VideoFrame, bool, error) {
	if w == nil || w.sup == nil {
		return nil, false, fmt.Errorf("%w: %w", ErrScreenCaptureUnavailable, ErrWaylandCaptureClosed)
	}
	frame, changed, err := w.sup.capture(maxW, maxH, withNV12)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrScreenCaptureUnavailable, err)
	}
	return frame, changed, nil
}

// Close ends the portal session, destroys the PipeWire stream and stops the
// loop thread. It is idempotent.
func (w *waylandPortalCapture) Close() error {
	if w == nil || w.sup == nil {
		return nil
	}
	return w.sup.close()
}

func (w *waylandPortalCapture) WaylandCaptureStatus() WaylandCaptureStatus {
	return w.sup.WaylandCaptureStatus()
}

func (w *waylandPortalCapture) RetryWaylandAuthorization() error {
	return w.sup.RetryWaylandAuthorization()
}

func waylandControllerOf(c *ScreenCapturer) waylandController {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.native.(*waylandPortalCapture); ok && w != nil {
		return w
	}
	return nil
}
