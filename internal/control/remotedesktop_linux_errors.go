package control

// This file intentionally has no build constraint (the "_errors" suffix is not
// a GOOS tag) so that UI code on every platform can errors.Is against the
// Linux Wayland remote-input errors without build tags.

import (
	"context"
	"errors"
)

var (
	// ErrWaylandRemoteInputUnavailable means the session is Wayland but the
	// xdg-desktop-portal RemoteDesktop interface is missing (no portal
	// service, or a portal backend without RemoteDesktop such as
	// xdg-desktop-portal-wlr/-lxqt) and no working fallback exists.
	ErrWaylandRemoteInputUnavailable = errors.New("control: Wayland 远程输入不可用 (xdg-desktop-portal RemoteDesktop is unavailable)")
	// ErrWaylandRemoteInputPending means the portal authorization dialog is
	// waiting for the student to allow remote control. Input is dropped (not
	// faked) until the user answers.
	ErrWaylandRemoteInputPending = errors.New("control: waiting for the student to authorize Wayland remote input")
	// ErrWaylandRemoteInputDenied means the student cancelled or denied the
	// portal remote-desktop dialog.
	ErrWaylandRemoteInputDenied = errors.New("control: Wayland remote input authorization was denied")
	// ErrWaylandRemoteInputClosed means an authorized portal session was
	// closed by the compositor (user revoked it, session switch, portal
	// restart). The next input re-requests authorization.
	ErrWaylandRemoteInputClosed = errors.New("control: Wayland remote input session was closed")
	// ErrWaylandRemoteInputDeviceDenied means the user granted the session
	// but not the required device type (keyboard or pointer).
	ErrWaylandRemoteInputDeviceDenied = errors.New("control: Wayland remote input device was not granted")
	// ErrWaylandAbsolutePointerUnavailable means the portal did not provide a
	// screen-cast stream for absolute pointer motion and the screen size is
	// unknown, so the pointer cannot be positioned.
	ErrWaylandAbsolutePointerUnavailable = errors.New("control: Wayland absolute pointer positioning is unavailable")
)

// WaylandInputState describes the Linux Wayland portal remote-input session.
type WaylandInputState string

const (
	WaylandInputIdle        WaylandInputState = "idle"
	WaylandInputAuthorizing WaylandInputState = "authorizing"
	WaylandInputActive      WaylandInputState = "active"
	WaylandInputDenied      WaylandInputState = "denied"
	WaylandInputUnavailable WaylandInputState = "unavailable"
	WaylandInputFailed      WaylandInputState = "failed"
)

// WaylandInputStatus is a snapshot of the portal remote-input session.
type WaylandInputStatus struct {
	State     WaylandInputState
	LastError error
	// Keyboard / Pointer report which device types the user granted.
	Keyboard bool
	Pointer  bool
	// AbsolutePointer is true when a screen-cast stream allows absolute
	// pointer positioning.
	AbsolutePointer bool
}

// WaylandInputAuthorizer is implemented by input backends that need an
// interactive desktop authorization before injecting input (the Linux
// Wayland xdg-desktop-portal backend). UI code can type-assert an
// InputBackend to it to request authorization up-front (for example when the
// teacher starts remote control) and to show the pending/denied state.
type WaylandInputAuthorizer interface {
	// AuthorizeRemoteInput starts (if needed) and waits for the portal
	// session. It returns nil once input can be injected.
	AuthorizeRemoteInput(ctx context.Context) error
	WaylandInputStatus() WaylandInputStatus
}
