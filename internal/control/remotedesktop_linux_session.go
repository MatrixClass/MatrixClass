package control

// OS-independent session state machine for the Linux Wayland
// xdg-desktop-portal RemoteDesktop input backend. The D-Bus adapter lives in
// remotedesktop_linux.go; this file only depends on the rdPortal interface so
// that authorization, denial, revocation and retry behaviour are unit-tested on
// every platform. (The "_session" suffix is not a GOOS tag.)

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// rdSessionInfo is the result of a successful portal Start.
type rdSessionInfo struct {
	devices       uint32
	stream        uint32
	hasStream     bool
	width, height int32
	restoreToken  string
}

// rdPortal is one authorized (or authorizing) portal remote-desktop session.
type rdPortal interface {
	// Establish runs CreateSession -> SelectDevices (-> ScreenCast
	// SelectSources) -> Start and blocks until the user answers the dialog,
	// ctx ends, or the portal fails.
	Establish(ctx context.Context, restoreToken string) (rdSessionInfo, error)
	PointerMotion(dx, dy float64) error
	PointerMotionAbsolute(stream uint32, x, y float64) error
	PointerButton(button int32, pressed bool) error
	PointerAxisDiscrete(axis uint32, steps int32) error
	KeyboardKeycode(keycode int32, pressed bool) error
	// Done is closed when the portal session or the bus connection ends.
	Done() <-chan struct{}
	Close() error
}

const (
	rdAuthorizeTimeout   = 3 * time.Minute
	rdRetryAfterDenied   = time.Minute
	rdRetryAfterFailure  = 5 * time.Second
	rdRetryAfterNoPortal = time.Minute
)

type rdInputBackend struct {
	mu sync.Mutex

	newPortal func() (rdPortal, error)
	loadToken func() string
	saveToken func(string) error
	// extent returns the logical screen size used for relative-motion
	// positioning when the portal provides no screen-cast stream.
	extent           func() (int32, int32, bool)
	now              func() time.Time
	authorizeTimeout time.Duration

	state    WaylandInputState
	lastErr  error
	lastFail time.Time
	portal   rdPortal
	info     rdSessionInfo
	pending  chan struct{}
	cancel   context.CancelFunc
	posKnown bool
	posX     float64
	posY     float64
	closed   bool
}

func newRDInputBackend(newPortal func() (rdPortal, error)) *rdInputBackend {
	return &rdInputBackend{
		newPortal:        newPortal,
		now:              time.Now,
		authorizeTimeout: rdAuthorizeTimeout,
		state:            WaylandInputIdle,
	}
}

// SendInputs injects inputs through the portal. Before the student has
// authorized the session it starts the authorization in the background and
// returns ErrWaylandRemoteInputPending; input is never reported as delivered
// unless the portal accepted it.
func (b *rdInputBackend) SendInputs(inputs []WinInput) error {
	actions := make([]rdAction, 0, len(inputs))
	for _, input := range inputs {
		converted, err := rdActionsFromInput(input)
		if err != nil {
			return err
		}
		actions = append(actions, converted...)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrWaylandRemoteInputClosed
	}
	if b.state != WaylandInputActive || b.portal == nil {
		return b.notReadyLocked()
	}
	for _, action := range actions {
		if err := b.sendLocked(action); err != nil {
			return err
		}
	}
	return nil
}

// notReadyLocked starts authorization when allowed by the retry policy and
// returns the error describing why input cannot be delivered now.
func (b *rdInputBackend) notReadyLocked() error {
	switch b.state {
	case WaylandInputAuthorizing:
		return ErrWaylandRemoteInputPending
	case WaylandInputDenied, WaylandInputFailed, WaylandInputUnavailable:
		if b.now().Sub(b.lastFail) < b.retryDelayLocked() {
			return b.lastErr
		}
	}
	b.startLocked()
	if b.state == WaylandInputAuthorizing {
		return ErrWaylandRemoteInputPending
	}
	return b.lastErr
}

func (b *rdInputBackend) retryDelayLocked() time.Duration {
	switch b.state {
	case WaylandInputDenied:
		return rdRetryAfterDenied
	case WaylandInputUnavailable:
		return rdRetryAfterNoPortal
	default:
		return rdRetryAfterFailure
	}
}

// startLocked launches the portal handshake unless one is already running.
func (b *rdInputBackend) startLocked() chan struct{} {
	if b.state == WaylandInputAuthorizing && b.pending != nil {
		return b.pending
	}
	done := make(chan struct{})
	timeout := b.authorizeTimeout
	if timeout <= 0 {
		timeout = rdAuthorizeTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	b.pending = done
	b.cancel = cancel
	b.state = WaylandInputAuthorizing
	b.lastErr = ErrWaylandRemoteInputPending
	token := ""
	if b.loadToken != nil {
		token = b.loadToken()
	}
	go b.establish(ctx, cancel, done, token)
	return done
}

func (b *rdInputBackend) establish(ctx context.Context, cancel context.CancelFunc, done chan struct{}, token string) {
	defer cancel()
	defer close(done)
	if b.newPortal == nil {
		b.finish(done, nil, rdSessionInfo{}, ErrWaylandRemoteInputUnavailable)
		return
	}
	portal, err := b.newPortal()
	if err != nil {
		b.finish(done, nil, rdSessionInfo{}, err)
		return
	}
	info, err := portal.Establish(ctx, token)
	if err == nil && info.devices&(rdDeviceKeyboard|rdDevicePointer) == 0 {
		err = fmt.Errorf("%w: neither keyboard nor pointer", ErrWaylandRemoteInputDeviceDenied)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, ErrWaylandRemoteInputDenied) {
			err = fmt.Errorf("%w: authorization timed out", ErrWaylandRemoteInputDenied)
		}
		_ = portal.Close()
		b.finish(done, nil, rdSessionInfo{}, err)
		return
	}
	if info.restoreToken != "" && b.saveToken != nil {
		_ = b.saveToken(info.restoreToken)
	}
	b.finish(done, portal, info, nil)
}

func (b *rdInputBackend) finish(done chan struct{}, portal rdPortal, info rdSessionInfo, err error) {
	b.mu.Lock()
	if b.closed || b.pending != done {
		b.mu.Unlock()
		if portal != nil {
			_ = portal.Close()
		}
		return
	}
	b.pending = nil
	b.cancel = nil
	if err != nil {
		b.portal = nil
		b.lastErr = err
		b.lastFail = b.now()
		switch {
		case errors.Is(err, ErrWaylandRemoteInputUnavailable):
			b.state = WaylandInputUnavailable
		case errors.Is(err, ErrWaylandRemoteInputDenied), errors.Is(err, ErrWaylandRemoteInputDeviceDenied):
			b.state = WaylandInputDenied
		default:
			b.state = WaylandInputFailed
		}
		b.mu.Unlock()
		return
	}
	b.portal = portal
	b.info = info
	b.state = WaylandInputActive
	b.lastErr = nil
	b.posKnown = false
	b.mu.Unlock()
	go b.watch(portal)
}

// watch resets the backend when the compositor closes the session (user
// revoked access, session/VT switch, portal restart, bus disconnect).
func (b *rdInputBackend) watch(portal rdPortal) {
	<-portal.Done()
	b.mu.Lock()
	if b.portal == portal {
		b.markClosedLocked()
	}
	b.mu.Unlock()
	_ = portal.Close()
}

func (b *rdInputBackend) markClosedLocked() {
	b.portal = nil
	b.info = rdSessionInfo{}
	b.state = WaylandInputIdle
	b.lastErr = ErrWaylandRemoteInputClosed
	b.posKnown = false
}

func (b *rdInputBackend) sendLocked(action rdAction) error {
	portal := b.portal
	var err error
	switch action.kind {
	case rdKeyboardKey:
		if b.info.devices&rdDeviceKeyboard == 0 {
			return fmt.Errorf("%w: keyboard", ErrWaylandRemoteInputDeviceDenied)
		}
		err = portal.KeyboardKeycode(action.keycode, action.pressed)
	case rdPointerButton, rdPointerAxis, rdPointerAbsolute:
		if b.info.devices&rdDevicePointer == 0 {
			return fmt.Errorf("%w: pointer", ErrWaylandRemoteInputDeviceDenied)
		}
		switch action.kind {
		case rdPointerButton:
			err = portal.PointerButton(action.button, action.pressed)
		case rdPointerAxis:
			if action.steps == 0 {
				return nil
			}
			err = portal.PointerAxisDiscrete(action.axis, action.steps)
		default:
			err = b.moveLocked(portal, action.x, action.y)
		}
	default:
		return ErrInvalidInputKind
	}
	if err == nil {
		return nil
	}
	select {
	case <-portal.Done():
		b.markClosedLocked()
		return fmt.Errorf("%w: %v", ErrWaylandRemoteInputClosed, err)
	default:
	}
	return fmt.Errorf("control: Wayland portal input failed: %w", err)
}

func (b *rdInputBackend) moveLocked(portal rdPortal, nx, ny float64) error {
	if b.info.hasStream && b.info.width > 0 && b.info.height > 0 {
		return portal.PointerMotionAbsolute(b.info.stream, rdStreamCoordinate(nx, b.info.width), rdStreamCoordinate(ny, b.info.height))
	}
	// No stream: emulate absolute positioning with relative motion. The
	// pointer is first pushed into the top-left corner (compositors clamp
	// it), then moved by deltas from the tracked position.
	var width, height int32
	var ok bool
	if b.extent != nil {
		width, height, ok = b.extent()
	}
	if !ok || width <= 0 || height <= 0 {
		return ErrWaylandAbsolutePointerUnavailable
	}
	if !b.posKnown {
		home := -4 * math.Max(float64(width), float64(height))
		if err := portal.PointerMotion(home, home); err != nil {
			return err
		}
		b.posX, b.posY, b.posKnown = 0, 0, true
	}
	tx, ty := rdStreamCoordinate(nx, width), rdStreamCoordinate(ny, height)
	if err := portal.PointerMotion(tx-b.posX, ty-b.posY); err != nil {
		b.posKnown = false
		return err
	}
	b.posX, b.posY = tx, ty
	return nil
}

// AuthorizeRemoteInput requests portal authorization now (ignoring the
// retry back-off) and waits until the student answers or ctx ends.
func (b *rdInputBackend) AuthorizeRemoteInput(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrWaylandRemoteInputClosed
	}
	if b.state == WaylandInputActive && b.portal != nil {
		b.mu.Unlock()
		return nil
	}
	done := b.startLocked()
	b.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == WaylandInputActive {
		return nil
	}
	return b.lastErr
}

func (b *rdInputBackend) WaylandInputStatus() WaylandInputStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	status := WaylandInputStatus{State: b.state, LastError: b.lastErr}
	if b.state == WaylandInputActive {
		status.Keyboard = b.info.devices&rdDeviceKeyboard != 0
		status.Pointer = b.info.devices&rdDevicePointer != 0
		status.AbsolutePointer = b.info.hasStream && b.info.width > 0 && b.info.height > 0
	}
	return status
}

// Close ends the portal session and any pending authorization.
func (b *rdInputBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	portal := b.portal
	cancel := b.cancel
	b.portal = nil
	b.pending = nil
	b.cancel = nil
	b.state = WaylandInputIdle
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if portal != nil {
		return portal.Close()
	}
	return nil
}
