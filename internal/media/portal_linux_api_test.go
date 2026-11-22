package media

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeWaylandTokens struct {
	mu    sync.Mutex
	token string
	saved []string
}

func (f *fakeWaylandTokens) load() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

func (f *fakeWaylandTokens) save(token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = token
	f.saved = append(f.saved, token)
	return nil
}

type fakeWaylandGrant struct {
	token     string
	closedCh  chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	opens     int
	released  bool
}

func newFakeGrant(token string) *fakeWaylandGrant {
	return &fakeWaylandGrant{token: token, closedCh: make(chan struct{})}
}

func (g *fakeWaylandGrant) stream() portalStream {
	return portalStream{NodeID: 42, SourceType: portalSourceMonitor, Width: 4, Height: 2}
}
func (g *fakeWaylandGrant) restoreToken() string    { return g.token }
func (g *fakeWaylandGrant) portalVersion() uint32   { return 5 }
func (g *fakeWaylandGrant) closed() <-chan struct{} { return g.closedCh }
func (g *fakeWaylandGrant) openPipeWire(context.Context) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.opens++
	return 100 + g.opens, nil
}
func (g *fakeWaylandGrant) close() {
	g.mu.Lock()
	g.released = true
	g.mu.Unlock()
}
func (g *fakeWaylandGrant) revoke() { g.closeOnce.Do(func() { close(g.closedCh) }) }

type fakeWaylandDriver struct {
	mu           sync.Mutex
	availableErr error
	authErrs     []error
	authTokens   []string
	grants       []*fakeWaylandGrant
	nextToken    int
	streamCalls  int
	streamFn     func(call int, ctx context.Context, sink *pwStreamSink) error
	shotAvailErr error
	shotCalls    int
	shotFn       func(call int) ([]byte, int, int, time.Duration, error)
}

func (d *fakeWaylandDriver) streamAvailable() error { return d.availableErr }

func (d *fakeWaylandDriver) authorize(ctx context.Context, token string) (waylandGrant, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authTokens = append(d.authTokens, token)
	if len(d.authErrs) > 0 {
		err := d.authErrs[0]
		d.authErrs = d.authErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	d.nextToken++
	g := newFakeGrant("tok-" + string(rune('0'+d.nextToken)))
	d.grants = append(d.grants, g)
	return g, nil
}

func (d *fakeWaylandDriver) stream(ctx context.Context, fd int, nodeID uint32, sink *pwStreamSink) error {
	d.mu.Lock()
	d.streamCalls++
	call := d.streamCalls
	fn := d.streamFn
	d.mu.Unlock()
	if fn == nil {
		<-ctx.Done()
		return nil
	}
	return fn(call, ctx, sink)
}

func (d *fakeWaylandDriver) screenshotAvailable(context.Context) error { return d.shotAvailErr }

func (d *fakeWaylandDriver) screenshot(ctx context.Context) ([]byte, int, int, error) {
	d.mu.Lock()
	d.shotCalls++
	call := d.shotCalls
	d.mu.Unlock()
	rgba, w, h, delay, err := d.shotFn(call)
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, 0, 0, ctx.Err()
		}
	}
	return rgba, w, h, err
}

func (d *fakeWaylandDriver) snapshot() (auths []string, grants []*fakeWaylandGrant, streams, shots int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.authTokens...), append([]*fakeWaylandGrant(nil), d.grants...), d.streamCalls, d.shotCalls
}

var waylandTestTiming = waylandTiming{
	minBackoff:            2 * time.Millisecond,
	maxBackoff:            10 * time.Millisecond,
	pipeWireFailures:      2,
	screenshotInterval:    0,
	screenshotInteractive: 80 * time.Millisecond,
}

func waitWayland(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// streamOneFrame emulates a compositor: STREAMING, one 4x2 BGRx frame with
// row padding, then either an error or blocking until ctx ends.
func streamOneFrame(fill byte, result error) func(int, context.Context, *pwStreamSink) error {
	return func(_ int, ctx context.Context, sink *pwStreamSink) error {
		sink.onState(true, false)
		sink.onFormat(pwVideoFormatInfo{Format: pwVideoFormatBGRx, Width: 4, Height: 2})
		stride := 4*4 + 8
		raw := bytes.Repeat([]byte{fill, 0x20, 0x30, 0x00}, 0)
		for row := 0; row < 2; row++ {
			line := bytes.Repeat([]byte{fill, 0x20, 0x30, 0x00}, 4)
			raw = append(raw, line...)
			if row == 0 {
				raw = append(raw, make([]byte, stride-len(line))...)
			}
		}
		if err := sink.frames.store(raw, 4, 2, stride, pwVideoFormatBGRx); err != nil {
			return err
		}
		sink.onFrame()
		if result != nil {
			return result
		}
		<-ctx.Done()
		return nil
	}
}

func TestWaylandSupervisorAuthorizesStreamsAndCloses(t *testing.T) {
	driver := &fakeWaylandDriver{streamFn: streamOneFrame(0x10, nil)}
	tokens := &fakeWaylandTokens{token: "tok-old"}
	sup := newWaylandSupervisor(driver, tokens, waylandTestTiming, false)
	if st := sup.WaylandCaptureStatus(); st.State != WaylandCaptureIdle || !st.RestoreToken {
		t.Fatalf("initial status %+v", st)
	}
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandAuthorizationPending) && !errors.Is(err, ErrWaylandNoFrameYet) {
		t.Fatalf("first capture should report pending authorization, got %v", err)
	}
	var frameData []byte
	waitWayland(t, "first frame", func() bool {
		frame, changed, err := sup.capture(0, 0, false)
		if err != nil {
			return false
		}
		if !changed || frame.Width != 4 || frame.Height != 2 {
			t.Fatalf("unexpected frame %dx%d changed=%v", frame.Width, frame.Height, changed)
		}
		frameData = frame.Data
		return true
	})
	if !bytes.Equal(frameData[:4], []byte{0x30, 0x20, 0x10, 0xff}) {
		t.Fatalf("BGRx not converted to RGBA: %x", frameData[:4])
	}
	frame, changed, err := sup.capture(0, 0, true)
	if err != nil || changed || len(frame.NV12Y) == 0 {
		t.Fatalf("second capture of same frame: changed=%v nv12=%d err=%v", changed, len(frame.NV12Y), err)
	}
	if frame, _, _ := sup.capture(2, 2, false); frame.Width > 2 || frame.Height > 2 {
		t.Fatalf("not downscaled: %dx%d", frame.Width, frame.Height)
	}
	st := sup.WaylandCaptureStatus()
	if st.State != WaylandCaptureRunning || !st.Authorized || !st.Connected || st.Backend != WaylandBackendPipeWire || st.NodeID != 42 || st.Frames != 1 || st.PortalVersion != 5 {
		t.Fatalf("running status %+v", st)
	}
	auths, grants, _, _ := driver.snapshot()
	if len(auths) != 1 || auths[0] != "tok-old" {
		t.Fatalf("restore token not passed to portal: %v", auths)
	}
	if tokens.load() != "tok-1" {
		t.Fatalf("new restore token not persisted: %q", tokens.load())
	}
	if err := sup.close(); err != nil {
		t.Fatal(err)
	}
	if err := sup.close(); err != nil {
		t.Fatal(err)
	}
	if !grants[0].released {
		t.Fatal("portal session not closed")
	}
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandCaptureClosed) {
		t.Fatalf("capture after close: %v", err)
	}
	if err := sup.RetryWaylandAuthorization(); !errors.Is(err, ErrWaylandCaptureClosed) {
		t.Fatalf("retry after close: %v", err)
	}
}

func TestWaylandSupervisorCancelWaitsForUserRetry(t *testing.T) {
	driver := &fakeWaylandDriver{authErrs: []error{ErrWaylandPortalCancelled, ErrWaylandPortalPermission}, streamFn: streamOneFrame(0x07, nil)}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, true)
	defer sup.close()
	if err := sup.RetryWaylandAuthorization(); err != nil {
		t.Fatal(err)
	}
	waitWayland(t, "cancelled state", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureCancelled })
	st := sup.WaylandCaptureStatus()
	if !st.NeedsUserAction || st.Authorized {
		t.Fatalf("cancelled status %+v", st)
	}
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandPortalCancelled) {
		t.Fatalf("capture while cancelled: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if auths, _, _, shots := driver.snapshot(); len(auths) != 1 || shots != 0 {
		t.Fatalf("cancel must not re-prompt or fall back automatically: auths=%d shots=%d", len(auths), shots)
	}
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "denied state", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureDenied })
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandPortalPermission) {
		t.Fatalf("capture while denied: %v", err)
	}
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "authorized after retry", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureRunning })
	if auths, _, _, _ := driver.snapshot(); len(auths) != 3 {
		t.Fatalf("expected 3 authorizations, got %d", len(auths))
	}
}

func TestWaylandSupervisorReconnectsPipeWireWithinSession(t *testing.T) {
	driver := &fakeWaylandDriver{}
	driver.streamFn = func(call int, ctx context.Context, sink *pwStreamSink) error {
		if call == 1 {
			return streamOneFrame(0x01, ErrWaylandPipeWireDisconnected)(call, ctx, sink)
		}
		return streamOneFrame(0x02, nil)(call, ctx, sink)
	}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "second stream", func() bool {
		_, _, streams, _ := driver.snapshot()
		return streams >= 2 && sup.WaylandCaptureStatus().State == WaylandCaptureRunning
	})
	st := sup.WaylandCaptureStatus()
	if st.Reconnects != 1 {
		t.Fatalf("expected 1 reconnect, got %+v", st)
	}
	auths, grants, _, _ := driver.snapshot()
	if len(auths) != 1 || grants[0].opens != 2 {
		t.Fatalf("PipeWire reconnect must reuse the portal session: auths=%d opens=%d", len(auths), grants[0].opens)
	}
	waitWayland(t, "frame from reconnected stream", func() bool {
		frame, _, err := sup.capture(0, 0, false)
		return err == nil && frame.Data[2] == 0x02
	})
}

func TestWaylandSupervisorRecreatesSessionAfterRepeatedFramelessFailures(t *testing.T) {
	driver := &fakeWaylandDriver{}
	driver.streamFn = func(call int, ctx context.Context, sink *pwStreamSink) error {
		if call <= 2 {
			return ErrWaylandPipeWireDisconnected
		}
		return streamOneFrame(0x03, nil)(call, ctx, sink)
	}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "running after new session", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureRunning })
	auths, grants, _, _ := driver.snapshot()
	if len(auths) != 2 || !grants[0].released {
		t.Fatalf("expected a fresh portal session: auths=%d", len(auths))
	}
	if auths[1] != "tok-1" {
		t.Fatalf("new session must use the persisted restore token, got %q", auths[1])
	}
}

func TestWaylandSupervisorSessionClosedRestoresWithToken(t *testing.T) {
	driver := &fakeWaylandDriver{streamFn: streamOneFrame(0x04, nil)}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "running", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureRunning })
	_, grants, _, _ := driver.snapshot()
	grants[0].revoke()
	waitWayland(t, "restored session", func() bool {
		auths, _, _, _ := driver.snapshot()
		return len(auths) == 2 && sup.WaylandCaptureStatus().State == WaylandCaptureRunning
	})
	if auths, _, _, _ := driver.snapshot(); auths[1] != "tok-1" {
		t.Fatalf("restore must use the saved token, got %q", auths[1])
	}
}

func TestWaylandSupervisorSessionClosedWithoutTokenNeedsUser(t *testing.T) {
	driver := &fakeWaylandDriver{streamFn: streamOneFrame(0x05, nil)}
	tokens := &fakeWaylandTokens{}
	sup := newWaylandSupervisor(driver, tokens, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "running", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureRunning })
	_ = tokens.save("") // portal without persistence support
	_, grants, _, _ := driver.snapshot()
	grants[0].revoke()
	waitWayland(t, "stopped", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureStopped })
	st := sup.WaylandCaptureStatus()
	if !st.NeedsUserAction || !errors.Is(st.LastError, ErrWaylandSessionClosed) {
		t.Fatalf("status %+v", st)
	}
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandSessionClosed) {
		t.Fatalf("stale frame served after revoke: %v", err)
	}
}

func TestWaylandSupervisorScreenshotFallback(t *testing.T) {
	driver := &fakeWaylandDriver{availableErr: ErrWaylandPipeWireUnavailable}
	driver.shotFn = func(call int) ([]byte, int, int, time.Duration, error) {
		px := bytes.Repeat([]byte{byte(call), 2, 3, 255}, 4)
		if call == 1 {
			return px, 2, 2, 100 * time.Millisecond, nil // one-time prompt is fine
		}
		if call == 2 {
			return px, 2, 2, 0, nil
		}
		return px, 2, 2, 200 * time.Millisecond, nil // prompting every frame
	}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, true)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "screenshot frame", func() bool {
		frame, _, err := sup.capture(0, 0, false)
		return err == nil && frame.Width == 2 && frame.Data[0] == 1
	})
	if st := sup.WaylandCaptureStatus(); st.Backend != WaylandBackendScreenshot {
		t.Fatalf("backend %q", st.Backend)
	}
	waitWayland(t, "second screenshot", func() bool {
		frame, _, err := sup.capture(0, 0, false)
		return err == nil && frame.Data[0] == 2
	})
	waitWayland(t, "prompting detected", func() bool {
		_, _, _ = sup.capture(0, 0, false)
		return sup.WaylandCaptureStatus().State == WaylandCaptureDenied
	})
	st := sup.WaylandCaptureStatus()
	if !st.NeedsUserAction || !errors.Is(st.LastError, ErrWaylandPortalPermission) {
		t.Fatalf("status %+v", st)
	}
	if _, _, err := sup.capture(0, 0, false); err == nil {
		t.Fatal("frame served after fallback was rejected")
	}
}

func TestWaylandSupervisorNoBackendAtAll(t *testing.T) {
	driver := &fakeWaylandDriver{availableErr: ErrWaylandPipeWireUnavailable, shotAvailErr: errPortalInterfaceMissing}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, true)
	defer sup.close()
	_, _, _ = sup.capture(0, 0, false)
	waitWayland(t, "unavailable", func() bool { return sup.WaylandCaptureStatus().NeedsUserAction })
	st := sup.WaylandCaptureStatus()
	if st.State != WaylandCaptureUnavailable || !errors.Is(st.LastError, ErrWaylandPipeWireUnavailable) {
		t.Fatalf("status %+v", st)
	}
	if _, _, err := sup.capture(0, 0, false); !errors.Is(err, ErrWaylandPipeWireUnavailable) {
		t.Fatalf("capture: %v", err)
	}
}

func TestWaylandSupervisorPortalMissingRetriesWithBackoff(t *testing.T) {
	driver := &fakeWaylandDriver{authErrs: []error{ErrWaylandPortalUnavailable, ErrWaylandPortalUnavailable}, streamFn: streamOneFrame(0x08, nil)}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "running after portal appears", func() bool { return sup.WaylandCaptureStatus().State == WaylandCaptureRunning })
	if auths, _, _, _ := driver.snapshot(); len(auths) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(auths))
	}
}

func TestWaylandSupervisorPausedKeepsLastRealFrame(t *testing.T) {
	paused := make(chan struct{})
	driver := &fakeWaylandDriver{}
	driver.streamFn = func(call int, ctx context.Context, sink *pwStreamSink) error {
		_ = streamOneFrame(0x06, errors.New("stop"))(call, ctx, sink)
		<-paused
		sink.onState(false, true)
		<-ctx.Done()
		return nil
	}
	sup := newWaylandSupervisor(driver, &fakeWaylandTokens{}, waylandTestTiming, false)
	defer sup.close()
	_ = sup.RetryWaylandAuthorization()
	waitWayland(t, "frame", func() bool { _, _, err := sup.capture(0, 0, false); return err == nil })
	close(paused)
	waitWayland(t, "paused", func() bool { return sup.WaylandCaptureStatus().Paused })
	if _, changed, err := sup.capture(0, 0, false); err != nil || changed {
		t.Fatalf("paused capture: changed=%v err=%v", changed, err)
	}
}

func TestWaylandClassifyAndBlockingErrors(t *testing.T) {
	cases := []struct {
		err    error
		token  bool
		action waylandAction
		state  WaylandCaptureState
	}{
		{ErrWaylandPortalCancelled, true, waylandActionNeedUser, WaylandCaptureCancelled},
		{ErrWaylandPortalPermission, true, waylandActionNeedUser, WaylandCaptureDenied},
		{ErrWaylandSessionClosed, true, waylandActionRestore, WaylandCaptureReconnecting},
		{ErrWaylandSessionClosed, false, waylandActionNeedUser, WaylandCaptureStopped},
		{ErrWaylandPipeWireUnavailable, false, waylandActionFallback, WaylandCaptureUnavailable},
		{errPortalInterfaceMissing, false, waylandActionFallback, WaylandCaptureUnavailable},
		{ErrWaylandPortalUnavailable, false, waylandActionRetryLater, WaylandCaptureUnavailable},
		{errors.New("transient"), false, waylandActionRetryLater, WaylandCaptureReconnecting},
	}
	for _, c := range cases {
		action, state := waylandClassify(c.err, c.token)
		if action != c.action || state != c.state {
			t.Fatalf("%v (token=%v): got %v/%s want %v/%s", c.err, c.token, action, state, c.action, c.state)
		}
	}
	if !errors.Is(waylandBlockingError(WaylandCaptureStatus{State: WaylandCaptureAuthorizing}), ErrWaylandAuthorizationPending) {
		t.Fatal("authorizing")
	}
	if !errors.Is(waylandBlockingError(WaylandCaptureStatus{State: WaylandCaptureCancelled}), ErrWaylandPortalCancelled) {
		t.Fatal("cancelled")
	}
	if !errors.Is(waylandBlockingError(WaylandCaptureStatus{State: WaylandCaptureReconnecting}), ErrWaylandPipeWireDisconnected) {
		t.Fatal("reconnecting")
	}
}

func TestWaylandStatusAPIOnNonWaylandCapturer(t *testing.T) {
	c := NewScreenCapturer(CaptureOptions{Width: 8, Height: 8, ForceSynthetic: true})
	defer c.Close()
	if _, ok := c.WaylandStatus(); ok {
		t.Fatal("synthetic capturer reported Wayland status")
	}
	if err := c.RetryWaylandAuthorization(); !errors.Is(err, ErrWaylandPortalUnavailable) {
		t.Fatalf("retry on non-Wayland: %v", err)
	}
	var m *Manager
	if _, ok := m.WaylandCaptureStatus(); ok {
		t.Fatal("nil manager reported status")
	}
}
