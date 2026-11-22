package media

// Wayland (xdg-desktop-portal + PipeWire) capture supervisor and its public
// status API. No GOOS constraint: the state machine talks to the portal and to
// PipeWire only through the waylandDriver interface (implemented on Linux in
// portal_linux.go/pipewire_linux.go), so authorization, cancellation,
// reconnect and fallback behaviour is unit tested on every OS with fakes. On
// non-Linux platforms the status API simply reports "not a Wayland backend".

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"nexlab/internal/core"
)

var (
	ErrWaylandPortalUnavailable    = errors.New("media: Wayland desktop portal is unavailable")
	ErrWaylandPortalPermission     = errors.New("media: Wayland screen capture permission was denied")
	ErrWaylandPortalCancelled      = errors.New("media: Wayland screen capture authorization was cancelled")
	ErrWaylandPipeWireDisconnected = errors.New("media: Wayland PipeWire stream is disconnected")

	// ErrWaylandAuthorizationPending is returned while the portal dialog is
	// waiting for the user (or the restore token is being validated).
	ErrWaylandAuthorizationPending = errors.New("media: waiting for Wayland screen capture authorization")
	// ErrWaylandPipeWireUnavailable means frames cannot be consumed from
	// PipeWire on this system (library missing, unsupported architecture).
	ErrWaylandPipeWireUnavailable = errors.New("media: PipeWire screen capture is unavailable on this system")
	// ErrWaylandSessionClosed means the desktop ended the screen cast session
	// (user pressed "stop sharing", session switch, portal restart).
	ErrWaylandSessionClosed = errors.New("media: Wayland screen capture session was closed by the desktop")
	// ErrWaylandNoFrameYet means the stream is connected but the compositor has
	// not delivered the first frame yet.
	ErrWaylandNoFrameYet = errors.New("media: waiting for the first Wayland screen frame")
	// ErrWaylandCaptureClosed is returned after Close.
	ErrWaylandCaptureClosed = errors.New("media: Wayland screen capture is closed")
)

type WaylandCaptureState string

const (
	WaylandCaptureUnavailable  WaylandCaptureState = "unavailable"
	WaylandCaptureAuthorizing  WaylandCaptureState = "authorizing"
	WaylandCaptureRunning      WaylandCaptureState = "running"
	WaylandCaptureReconnecting WaylandCaptureState = "reconnecting"
	WaylandCaptureStopped      WaylandCaptureState = "stopped"
	// WaylandCaptureIdle: no capture requested yet (nothing shown to the user).
	WaylandCaptureIdle WaylandCaptureState = "idle"
	// WaylandCaptureCancelled: the user dismissed the portal dialog.
	WaylandCaptureCancelled WaylandCaptureState = "cancelled"
	// WaylandCaptureDenied: the portal refused (policy, "other" response).
	WaylandCaptureDenied WaylandCaptureState = "denied"
)

// Wayland capture backends reported in WaylandCaptureStatus.Backend.
const (
	WaylandBackendPipeWire   = "pipewire"
	WaylandBackendScreenshot = "screenshot"
)

// WaylandCaptureStatus describes the Portal/PipeWire boundary. It never claims
// a stream exists unless real frames can be delivered.
type WaylandCaptureStatus struct {
	State      WaylandCaptureState
	LastError  error
	Reconnects uint64
	Authorized bool
	Connected  bool

	// NeedsUserAction is true when capture stays stopped until the user (or
	// UI) calls RetryWaylandAuthorization (cancelled/denied/revoked).
	NeedsUserAction bool
	// Backend is WaylandBackendPipeWire or WaylandBackendScreenshot when known.
	Backend string
	// Paused is true while the compositor paused the stream (screen locked,
	// session switched); the last real frame is still reported as unchanged.
	Paused        bool
	PortalVersion uint32
	NodeID        uint32
	Width         int
	Height        int
	Frames        uint64
	LastFrameAt   time.Time
	// RestoreToken reports whether a persisted approval exists, i.e. the next
	// authorization should not show a dialog.
	RestoreToken bool
}

// WaylandStatus reports the Wayland capture state when this capturer uses the
// Wayland portal backend; ok is false for any other backend or platform.
func (c *ScreenCapturer) WaylandStatus() (WaylandCaptureStatus, bool) {
	if ctrl := waylandControllerOf(c); ctrl != nil {
		return ctrl.WaylandCaptureStatus(), true
	}
	return WaylandCaptureStatus{}, false
}

// RetryWaylandAuthorization starts (or restarts after cancel/deny/revoke) the
// portal authorization. It never blocks on the dialog.
func (c *ScreenCapturer) RetryWaylandAuthorization() error {
	if ctrl := waylandControllerOf(c); ctrl != nil {
		return ctrl.RetryWaylandAuthorization()
	}
	return ErrWaylandPortalUnavailable
}

// WaylandCaptureStatus forwards ScreenCapturer.WaylandStatus.
func (m *Manager) WaylandCaptureStatus() (WaylandCaptureStatus, bool) {
	if m == nil || m.capturer == nil {
		return WaylandCaptureStatus{}, false
	}
	return m.capturer.WaylandStatus()
}

// RetryWaylandAuthorization forwards ScreenCapturer.RetryWaylandAuthorization.
func (m *Manager) RetryWaylandAuthorization() error {
	if m == nil || m.capturer == nil {
		return ErrWaylandPortalUnavailable
	}
	return m.capturer.RetryWaylandAuthorization()
}

type waylandController interface {
	WaylandCaptureStatus() WaylandCaptureStatus
	RetryWaylandAuthorization() error
}

// --- driver boundary ---------------------------------------------------------

// waylandGrant is an authorized portal ScreenCast session.
type waylandGrant interface {
	stream() portalStream
	restoreToken() string
	portalVersion() uint32
	// openPipeWire returns a fresh PipeWire remote fd (ownership transfers to
	// the caller / PipeWire).
	openPipeWire(ctx context.Context) (int, error)
	// closed is closed when the portal reports Session.Closed.
	closed() <-chan struct{}
	close()
}

// pwStreamSink receives stream events from the native PipeWire loop thread.
type pwStreamSink struct {
	frames    *pwFrameStore
	onState   func(streaming, paused bool)
	onFormat  func(pwVideoFormatInfo)
	onFrame   func()
	onProblem func(error)
}

type waylandDriver interface {
	// streamAvailable reports whether PipeWire frames can be consumed at all.
	streamAvailable() error
	authorize(ctx context.Context, restoreToken string) (waylandGrant, error)
	// stream consumes nodeID over fd until ctx ends or the stream fails.
	stream(ctx context.Context, fd int, nodeID uint32, sink *pwStreamSink) error
	// screenshotAvailable reports whether the Screenshot portal exists.
	screenshotAvailable(ctx context.Context) error
	// screenshot takes one non-interactive portal screenshot as RGBA.
	screenshot(ctx context.Context) (rgba []byte, width, height int, err error)
}

type waylandTokenStore interface {
	load() string
	save(token string) error
}

// --- supervisor --------------------------------------------------------------

type waylandTiming struct {
	minBackoff time.Duration
	maxBackoff time.Duration
	// pipeWireFailures is the number of consecutive frameless PipeWire
	// reconnects before the whole portal session is recreated.
	pipeWireFailures int
	// screenshotInterval bounds the fallback rate (low fps by design).
	screenshotInterval time.Duration
	// screenshotInteractive marks a screenshot call as "the portal prompted".
	screenshotInteractive time.Duration
}

var waylandDefaultTiming = waylandTiming{
	minBackoff:            time.Second,
	maxBackoff:            30 * time.Second,
	pipeWireFailures:      3,
	screenshotInterval:    2 * time.Second,
	screenshotInteractive: 1500 * time.Millisecond,
}

type waylandSupervisor struct {
	driver waylandDriver
	tokens waylandTokenStore
	timing waylandTiming
	// allowScreenshot enables the Screenshot portal fallback.
	allowScreenshot bool

	frames pwFrameStore

	statusMu sync.Mutex
	status   WaylandCaptureStatus

	mu        sync.Mutex
	started   bool
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	retry     chan struct{}
	shotReq   chan struct{}
	lastSeq   uint64
	lastShot  time.Time
	frameSeen uint64 // frames observed in the current PipeWire run
}

func newWaylandSupervisor(driver waylandDriver, tokens waylandTokenStore, timing waylandTiming, allowScreenshot bool) *waylandSupervisor {
	s := &waylandSupervisor{
		driver:          driver,
		tokens:          tokens,
		timing:          timing,
		allowScreenshot: allowScreenshot,
		retry:           make(chan struct{}, 1),
		shotReq:         make(chan struct{}, 1),
	}
	s.status = WaylandCaptureStatus{State: WaylandCaptureIdle, RestoreToken: tokens != nil && tokens.load() != ""}
	return s
}

func (s *waylandSupervisor) WaylandCaptureStatus() WaylandCaptureStatus {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

func (s *waylandSupervisor) update(fn func(*WaylandCaptureStatus)) {
	s.statusMu.Lock()
	fn(&s.status)
	s.statusMu.Unlock()
}

// RetryWaylandAuthorization starts the supervisor or wakes it from a state
// that waits for the user.
func (s *waylandSupervisor) RetryWaylandAuthorization() error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return ErrWaylandCaptureClosed
	}
	if s.ensureStarted() {
		return nil
	}
	select {
	case s.retry <- struct{}{}:
	default:
	}
	return nil
}

// ensureStarted launches the run loop once; it returns true if this call
// started it.
func (s *waylandSupervisor) ensureStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closed {
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.started, s.cancel, s.done = true, cancel, make(chan struct{})
	s.update(func(st *WaylandCaptureStatus) {
		st.State, st.LastError, st.NeedsUserAction = WaylandCaptureAuthorizing, ErrWaylandAuthorizationPending, false
	})
	go s.run(ctx, s.done)
	return true
}

// close stops everything idempotently and waits for the run loop to finish
// (which closes the portal session and destroys the PipeWire stream).
func (s *waylandSupervisor) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.frames.reset()
	s.update(func(st *WaylandCaptureStatus) {
		st.State, st.LastError, st.Connected, st.Paused, st.NeedsUserAction = WaylandCaptureStopped, ErrWaylandCaptureClosed, false, false, false
	})
	return nil
}

// captureBlockingError explains why no frame can be delivered in st.
func waylandBlockingError(st WaylandCaptureStatus) error {
	switch st.State {
	case WaylandCaptureIdle, WaylandCaptureAuthorizing:
		return ErrWaylandAuthorizationPending
	case WaylandCaptureRunning:
		return ErrWaylandNoFrameYet
	}
	if st.LastError != nil {
		return st.LastError
	}
	switch st.State {
	case WaylandCaptureCancelled:
		return ErrWaylandPortalCancelled
	case WaylandCaptureDenied:
		return ErrWaylandPortalPermission
	case WaylandCaptureReconnecting:
		return ErrWaylandPipeWireDisconnected
	case WaylandCaptureStopped:
		return ErrWaylandCaptureClosed
	}
	return ErrWaylandPortalUnavailable
}

// capture returns the newest real frame. It never blocks on authorization and
// never synthesizes content: without a real frame it returns an error.
func (s *waylandSupervisor) capture(maxW, maxH int, withNV12 bool) (*core.VideoFrame, bool, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, false, ErrWaylandCaptureClosed
	}
	s.ensureStarted()
	st := s.WaylandCaptureStatus()
	if st.State != WaylandCaptureRunning {
		return nil, false, waylandBlockingError(st)
	}
	if st.Backend == WaylandBackendScreenshot {
		s.requestScreenshot()
	}
	rgba, width, height, seq, ok, err := s.frames.snapshotRGBA()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, ErrWaylandNoFrameYet
	}
	now := time.Now()
	frame := &core.VideoFrame{Seq: uint64(now.UnixNano()), Codec: core.CodecFrameZstd, Width: width, Height: height, KeyFrame: true, Timestamp: now.UnixMilli(), Data: rgba}
	if (maxW > 0 && width > maxW) || (maxH > 0 && height > maxH) {
		frame = DownscaleFrame(frame, maxW, maxH)
	}
	if withNV12 {
		_ = EnsureNV12(frame)
	}
	s.mu.Lock()
	changed := seq != s.lastSeq
	s.lastSeq = seq
	s.mu.Unlock()
	return frame, changed, nil
}

func (s *waylandSupervisor) requestScreenshot() {
	s.mu.Lock()
	due := time.Since(s.lastShot) >= s.timing.screenshotInterval
	s.mu.Unlock()
	if !due {
		return
	}
	select {
	case s.shotReq <- struct{}{}:
	default:
	}
}

type waylandAction int

const (
	waylandActionRetryLater waylandAction = iota
	waylandActionNeedUser
	waylandActionFallback
	waylandActionRestore
)

// waylandClassify decides how the run loop reacts to a session failure.
func waylandClassify(err error, haveRestoreToken bool) (waylandAction, WaylandCaptureState) {
	switch {
	case errors.Is(err, ErrWaylandPortalCancelled):
		return waylandActionNeedUser, WaylandCaptureCancelled
	case errors.Is(err, ErrWaylandPortalPermission):
		return waylandActionNeedUser, WaylandCaptureDenied
	case errors.Is(err, ErrWaylandSessionClosed):
		if haveRestoreToken {
			// A persisted approval restores silently; without it a dialog would
			// pop up unexpectedly, so wait for the user instead.
			return waylandActionRestore, WaylandCaptureReconnecting
		}
		return waylandActionNeedUser, WaylandCaptureStopped
	case errors.Is(err, ErrWaylandPipeWireUnavailable), errors.Is(err, errPortalInterfaceMissing):
		return waylandActionFallback, WaylandCaptureUnavailable
	case errors.Is(err, ErrWaylandPortalUnavailable):
		return waylandActionRetryLater, WaylandCaptureUnavailable
	}
	return waylandActionRetryLater, WaylandCaptureReconnecting
}

func (s *waylandSupervisor) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := s.timing.minBackoff
	for ctx.Err() == nil {
		err := s.runSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			backoff = s.timing.minBackoff
			continue
		}
		action, state := waylandClassify(err, s.tokens != nil && s.tokens.load() != "")
		if action == waylandActionFallback && s.allowScreenshot {
			err = s.runScreenshot(ctx, err)
			if ctx.Err() != nil {
				return
			}
			action, state = waylandClassify(err, false)
			if action == waylandActionFallback {
				// Neither PipeWire nor a usable Screenshot portal: nothing will
				// change without a system update, so wait for an explicit retry.
				action = waylandActionNeedUser
			}
		} else if action == waylandActionFallback {
			action = waylandActionNeedUser
		}
		s.frames.reset()
		s.update(func(st *WaylandCaptureStatus) {
			st.State, st.LastError, st.Connected, st.Paused = state, err, false, false
			st.NeedsUserAction = action == waylandActionNeedUser
			if action == waylandActionNeedUser {
				st.Authorized = false
			}
		})
		switch action {
		case waylandActionNeedUser:
			// Drop retry requests made while capture was still working, so only
			// a request made after this failure restarts authorization.
			select {
			case <-s.retry:
			default:
			}
			select {
			case <-ctx.Done():
				return
			case <-s.retry:
				backoff = s.timing.minBackoff
			}
		default:
			if action == waylandActionRestore {
				s.update(func(st *WaylandCaptureStatus) { st.Reconnects++ })
			}
			if !s.sleep(ctx, backoff) {
				return
			}
			backoff = waylandNextBackoff(backoff, s.timing)
		}
		s.update(func(st *WaylandCaptureStatus) {
			st.State, st.LastError, st.NeedsUserAction = WaylandCaptureAuthorizing, ErrWaylandAuthorizationPending, false
		})
	}
}

func waylandNextBackoff(current time.Duration, timing waylandTiming) time.Duration {
	next := current * 2
	if next < timing.minBackoff {
		next = timing.minBackoff
	}
	if next > timing.maxBackoff {
		next = timing.maxBackoff
	}
	return next
}

// sleep waits for d, returning early (true) on an explicit retry and false
// when ctx ends.
func (s *waylandSupervisor) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.retry:
		return true
	case <-timer.C:
		return true
	}
}

// runSession performs one portal authorization and streams from PipeWire,
// transparently reconnecting the PipeWire remote while the session lives.
func (s *waylandSupervisor) runSession(ctx context.Context) error {
	if err := s.driver.streamAvailable(); err != nil {
		return err
	}
	token := ""
	if s.tokens != nil {
		token = s.tokens.load()
	}
	grant, err := s.driver.authorize(ctx, token)
	if err != nil {
		return err
	}
	defer grant.close()
	if s.tokens != nil {
		_ = s.tokens.save(grant.restoreToken())
	}
	src := grant.stream()
	s.update(func(st *WaylandCaptureStatus) {
		st.Authorized, st.Backend, st.PortalVersion = true, WaylandBackendPipeWire, grant.portalVersion()
		st.NodeID, st.Width, st.Height = src.NodeID, src.Width, src.Height
		st.RestoreToken = grant.restoreToken() != ""
		// Authorized but not streaming yet: UI shows "connecting".
		st.State, st.LastError = WaylandCaptureAuthorizing, nil
	})
	failures := 0
	backoff := s.timing.minBackoff
	for {
		fd, err := grant.openPipeWire(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-grant.closed():
				return ErrWaylandSessionClosed
			default:
			}
			return err
		}
		s.frames.reset()
		s.mu.Lock()
		s.frameSeen = 0
		s.mu.Unlock()
		streamCtx, cancel := context.WithCancel(ctx)
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			select {
			case <-grant.closed():
				cancel()
			case <-streamCtx.Done():
			}
		}()
		err = s.driver.stream(streamCtx, fd, src.NodeID, s.sink())
		cancel()
		<-watchDone
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-grant.closed():
			return ErrWaylandSessionClosed
		default:
		}
		if errors.Is(err, ErrWaylandPipeWireUnavailable) {
			return err
		}
		if err == nil {
			err = ErrWaylandPipeWireDisconnected
		}
		s.mu.Lock()
		gotFrames := s.frameSeen > 0
		s.mu.Unlock()
		if gotFrames {
			failures, backoff = 0, s.timing.minBackoff
		} else {
			failures++
		}
		if failures >= s.timing.pipeWireFailures {
			return fmt.Errorf("%w: %v", ErrWaylandPipeWireDisconnected, err)
		}
		s.frames.reset()
		s.update(func(st *WaylandCaptureStatus) {
			st.State, st.LastError, st.Connected, st.Paused = WaylandCaptureReconnecting, err, false, false
			st.Reconnects++
		})
		if !s.sleep(ctx, backoff) {
			return nil
		}
		backoff = waylandNextBackoff(backoff, s.timing)
	}
}

func (s *waylandSupervisor) sink() *pwStreamSink {
	return &pwStreamSink{
		frames: &s.frames,
		onState: func(streaming, paused bool) {
			s.update(func(st *WaylandCaptureStatus) {
				st.Connected = streaming || paused
				st.Paused = paused
				if streaming || paused {
					st.State, st.LastError = WaylandCaptureRunning, nil
				}
			})
		},
		onFormat: func(info pwVideoFormatInfo) {
			s.update(func(st *WaylandCaptureStatus) { st.Width, st.Height = info.Width, info.Height })
		},
		onFrame: func() {
			s.mu.Lock()
			s.frameSeen++
			s.mu.Unlock()
			now := time.Now()
			s.update(func(st *WaylandCaptureStatus) { st.Frames++; st.LastFrameAt = now })
		},
		onProblem: func(err error) {
			s.update(func(st *WaylandCaptureStatus) { st.LastError = err })
		},
	}
}

// runScreenshot serves frames from the non-interactive Screenshot portal at a
// low rate, driven by Capture() demand. It gives up (returning an error that
// requires the user) as soon as the portal turns out to prompt repeatedly.
func (s *waylandSupervisor) runScreenshot(ctx context.Context, cause error) error {
	if err := s.driver.screenshotAvailable(ctx); err != nil {
		return fmt.Errorf("%w; screenshot fallback unavailable: %v", cause, err)
	}
	s.frames.reset()
	s.update(func(st *WaylandCaptureStatus) {
		st.Backend, st.State, st.LastError, st.Connected, st.NeedsUserAction = WaylandBackendScreenshot, WaylandCaptureAuthorizing, ErrWaylandAuthorizationPending, false, false
	})
	shots := 0
	for {
		if shots > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-s.retry:
				// An explicit retry while on the fallback tries PipeWire again.
				return fmt.Errorf("%w: retrying PipeWire", ErrWaylandPortalUnavailable)
			case <-s.shotReq:
			}
		}
		s.mu.Lock()
		s.lastShot = time.Now()
		s.mu.Unlock()
		started := time.Now()
		rgba, width, height, err := s.driver.screenshot(ctx)
		elapsed := time.Since(started)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		// The first call may legitimately show a one-time permission prompt;
		// a slow call afterwards means the portal asks on every frame.
		if shots > 0 && elapsed > s.timing.screenshotInteractive {
			return fmt.Errorf("%w: the Screenshot portal prompts for every frame; authorization required", ErrWaylandPortalPermission)
		}
		if err := s.frames.store(rgba, width, height, width*4, pwVideoFormatRGBA); err != nil {
			return err
		}
		shots++
		now := time.Now()
		s.update(func(st *WaylandCaptureStatus) {
			st.State, st.LastError, st.Authorized, st.Connected = WaylandCaptureRunning, nil, true, true
			st.Width, st.Height = width, height
			st.Frames++
			st.LastFrameAt = now
		})
	}
}
