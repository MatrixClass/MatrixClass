//go:build linux

package media

import (
	"errors"
	"sync"
)

var (
	ErrWaylandPortalUnavailable = errors.New("media: Wayland desktop portal is unavailable")
	ErrWaylandPortalPermission = errors.New("media: Wayland screen capture permission was denied")
	ErrWaylandPortalCancelled = errors.New("media: Wayland screen capture authorization was cancelled")
	ErrWaylandPipeWireDisconnected = errors.New("media: Wayland PipeWire stream is disconnected")
)

type WaylandCaptureState string

const (
	WaylandCaptureUnavailable WaylandCaptureState = "unavailable"
	WaylandCaptureAuthorizing WaylandCaptureState = "authorizing"
	WaylandCaptureRunning     WaylandCaptureState = "running"
	WaylandCaptureReconnecting WaylandCaptureState = "reconnecting"
	WaylandCaptureStopped     WaylandCaptureState = "stopped"
)

// WaylandCaptureStatus describes the Portal/PipeWire boundary without
// pretending that a stream exists when the native bridge is not present.
type WaylandCaptureStatus struct {
	State       WaylandCaptureState
	LastError   error
	Reconnects  uint64
	Authorized  bool
	Connected   bool
}

type waylandCaptureSession struct {
	mu sync.Mutex
	status WaylandCaptureStatus
}

var waylandSessions sync.Map

func waylandSession(c *waylandCapture) *waylandCaptureSession {
	if c == nil {
		return nil
	}
	if existing, ok := waylandSessions.Load(c); ok {
		return existing.(*waylandCaptureSession)
	}
	session := &waylandCaptureSession{status: WaylandCaptureStatus{State: WaylandCaptureUnavailable, LastError: ErrWaylandPortalUnavailable}}
	actual, _ := waylandSessions.LoadOrStore(c, session)
	return actual.(*waylandCaptureSession)
}

// StartWaylandCapture begins the authorization boundary. The actual DBus
// Portal/PipeWire bridge is intentionally not claimed until implemented.
func (w *waylandCapture) StartWaylandCapture() error {
	session := waylandSession(w)
	if session == nil {
		return ErrWaylandPortalUnavailable
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.status.State = WaylandCaptureAuthorizing
	session.status.Authorized = false
	session.status.Connected = false
	session.status.LastError = ErrWaylandPortalUnavailable
	return ErrWaylandPortalUnavailable
}

// AuthorizeWaylandCapture records the Portal result. Successful authorization
// still requires the native PipeWire bridge before frames can be emitted.
func (w *waylandCapture) AuthorizeWaylandCapture(authorized bool) error {
	session := waylandSession(w)
	if session == nil {
		return ErrWaylandPortalUnavailable
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !authorized {
		session.status.State = WaylandCaptureStopped
		session.status.LastError = ErrWaylandPortalCancelled
		return ErrWaylandPortalCancelled
	}
	session.status.Authorized = true
	session.status.Connected = false
	session.status.State = WaylandCaptureUnavailable
	session.status.LastError = ErrWaylandPipeWireDisconnected
	return ErrWaylandPipeWireDisconnected
}

func (w *waylandCapture) MarkWaylandPipeWireDisconnected() error {
	session := waylandSession(w)
	if session == nil {
		return ErrWaylandPortalUnavailable
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.status.State = WaylandCaptureReconnecting
	session.status.Connected = false
	session.status.LastError = ErrWaylandPipeWireDisconnected
	return ErrWaylandPipeWireDisconnected
}

func (w *waylandCapture) ReconnectWaylandCapture() error {
	session := waylandSession(w)
	if session == nil {
		return ErrWaylandPortalUnavailable
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	session.status.Reconnects++
	session.status.Connected = false
	session.status.State = WaylandCaptureReconnecting
	session.status.LastError = ErrWaylandPipeWireDisconnected
	return ErrWaylandPipeWireDisconnected
}

func (w *waylandCapture) WaylandCaptureStatus() WaylandCaptureStatus {
	session := waylandSession(w)
	if session == nil {
		return WaylandCaptureStatus{State: WaylandCaptureUnavailable, LastError: ErrWaylandPortalUnavailable}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.status
}

