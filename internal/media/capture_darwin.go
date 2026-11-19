//go:build darwin

package media

import (
	"errors"
	"fmt"
	"sync"

	"nexlab/internal/core"
)

var (
	ErrDarwinScreenCaptureUnavailable = errors.New("media: ScreenCaptureKit capture is unavailable; native Darwin bridge is not enabled")
	ErrDarwinScreenCapturePermission  = errors.New("media: ScreenCaptureKit permission is required")
)

type DarwinScreenCaptureState string

const (
	DarwinScreenCaptureStateUnavailable  DarwinScreenCaptureState = "unavailable"
	DarwinScreenCaptureStateStarting     DarwinScreenCaptureState = "starting"
	DarwinScreenCaptureStateRunning      DarwinScreenCaptureState = "running"
	DarwinScreenCaptureStateReconnecting DarwinScreenCaptureState = "reconnecting"
	DarwinScreenCaptureStateStopped      DarwinScreenCaptureState = "stopped"
)

// DarwinScreenCaptureStream is the lifecycle boundary for a future
// ScreenCaptureKit bridge. The current pure-Go build deliberately remains
// unavailable instead of returning synthetic frames as real screen data.
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
	s.state = DarwinScreenCaptureStateUnavailable
	return ErrDarwinScreenCaptureUnavailable
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

type darwinScreenCaptureUnavailable struct{}

func (darwinScreenCaptureUnavailable) Backend() CaptureBackend { return CaptureBackendUnavailable }

func (darwinScreenCaptureUnavailable) Capture(int, int, bool) (*core.VideoFrame, bool, error) {
	return nil, false, ErrDarwinScreenCaptureUnavailable
}

func (darwinScreenCaptureUnavailable) Close() error { return nil }

func newNativeScreenCapture(CaptureOptions) nativeScreenCapture {
	return darwinScreenCaptureUnavailable{}
}

func (darwinScreenCaptureUnavailable) String() string {
	return fmt.Sprintf("%s", ErrDarwinScreenCaptureUnavailable)
}
