//go:build darwin

package media

import (
	"errors"
	"fmt"
	"sync"

	"nexlab/internal/core"
)

var (
	ErrDarwinScreenCaptureUnavailable = errors.New("media: macOS screen capture is unavailable")
	ErrDarwinScreenCapturePermission  = errors.New("media: macOS Screen Recording permission is required")
)

type DarwinScreenCaptureState string

const (
	DarwinScreenCaptureStateUnavailable  DarwinScreenCaptureState = "unavailable"
	DarwinScreenCaptureStateStarting     DarwinScreenCaptureState = "starting"
	DarwinScreenCaptureStateRunning      DarwinScreenCaptureState = "running"
	DarwinScreenCaptureStateReconnecting DarwinScreenCaptureState = "reconnecting"
	DarwinScreenCaptureStateStopped      DarwinScreenCaptureState = "stopped"
)

// DarwinScreenCaptureStream is the lifecycle boundary for the native macOS
// screen capture bridge (ScreenCaptureKit on macOS 14+, CoreGraphics on 12-13).
// Start probes the bridge without prompting: it reports Running only when the
// frameworks are present and Screen Recording access is granted, and never
// substitutes synthetic frames for real screen data. Errors always wrap
// ErrDarwinScreenCaptureUnavailable; missing access additionally wraps
// ErrDarwinScreenCapturePermission.
type DarwinScreenCaptureStream struct {
	mu                sync.Mutex
	opts              CaptureOptions
	state             DarwinScreenCaptureState
	reconnectAttempts uint32
	closed            bool
}

func NewDarwinScreenCaptureStream(opts CaptureOptions) *DarwinScreenCaptureStream {
	return &DarwinScreenCaptureStream{
		opts:  opts,
		state: DarwinScreenCaptureStateUnavailable,
	}
}

func (s *DarwinScreenCaptureStream) State() DarwinScreenCaptureState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *DarwinScreenCaptureStream) ReconnectAttempts() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconnectAttempts
}

func (s *DarwinScreenCaptureStream) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrDarwinScreenCaptureUnavailable
	}
	s.state = DarwinScreenCaptureStateStarting
	if _, err := DarwinScreenCaptureProbe(); err != nil {
		s.state = DarwinScreenCaptureStateUnavailable
		return err
	}
	s.state = DarwinScreenCaptureStateRunning
	return nil
}

func (s *DarwinScreenCaptureStream) Reconnect() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrDarwinScreenCaptureUnavailable
	}
	s.reconnectAttempts++
	s.state = DarwinScreenCaptureStateReconnecting
	s.mu.Unlock()
	return s.Start()
}

func (s *DarwinScreenCaptureStream) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.state = DarwinScreenCaptureStateStopped
	}
	return nil
}

func (s *DarwinScreenCaptureStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state = DarwinScreenCaptureStateStopped
	return nil
}

// darwinScreenCaptureUnavailable is returned when the native frameworks cannot
// be loaded at all (missing symbols, unsupported macOS).
type darwinScreenCaptureUnavailable struct{ err error }

func (darwinScreenCaptureUnavailable) Backend() CaptureBackend { return CaptureBackendUnavailable }

func (u darwinScreenCaptureUnavailable) Capture(int, int, bool) (*core.VideoFrame, bool, error) {
	return nil, false, u.error()
}

func (darwinScreenCaptureUnavailable) Close() error { return nil }

func (u darwinScreenCaptureUnavailable) error() error {
	if u.err == nil {
		return ErrDarwinScreenCaptureUnavailable
	}
	return u.err
}

func newNativeScreenCapture(opts CaptureOptions) nativeScreenCapture {
	return newDarwinScreenCapture(opts)
}

func (u darwinScreenCaptureUnavailable) String() string {
	return fmt.Sprintf("%v", u.error())
}
