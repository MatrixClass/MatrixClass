package control

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"nexlab/internal/core"
)

type fakeRDPortal struct {
	mu        sync.Mutex
	info      rdSessionInfo
	err       error
	release   chan struct{} // when non-nil, Establish blocks until closed
	gotToken  string
	calls     []string
	notifyErr error
	done      chan struct{}
	closeOnce sync.Once
	closed    bool
}

func newFakeRDPortal(info rdSessionInfo, err error) *fakeRDPortal {
	return &fakeRDPortal{info: info, err: err, done: make(chan struct{})}
}

func (f *fakeRDPortal) Establish(ctx context.Context, token string) (rdSessionInfo, error) {
	f.mu.Lock()
	f.gotToken = token
	release := f.release
	f.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return rdSessionInfo{}, ctx.Err()
		}
	}
	return f.info, f.err
}

func (f *fakeRDPortal) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.notifyErr
}

func (f *fakeRDPortal) PointerMotion(dx, dy float64) error {
	return f.record(fmt.Sprintf("rel %.0f %.0f", dx, dy))
}
func (f *fakeRDPortal) PointerMotionAbsolute(stream uint32, x, y float64) error {
	return f.record(fmt.Sprintf("abs %d %.0f %.0f", stream, x, y))
}
func (f *fakeRDPortal) PointerButton(button int32, pressed bool) error {
	return f.record(fmt.Sprintf("btn %#x %v", button, pressed))
}
func (f *fakeRDPortal) PointerAxisDiscrete(axis uint32, steps int32) error {
	return f.record(fmt.Sprintf("axis %d %d", axis, steps))
}
func (f *fakeRDPortal) KeyboardKeycode(keycode int32, pressed bool) error {
	return f.record(fmt.Sprintf("key %d %v", keycode, pressed))
}
func (f *fakeRDPortal) Done() <-chan struct{} { return f.done }
func (f *fakeRDPortal) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	f.revoke()
	return nil
}
func (f *fakeRDPortal) revoke() { f.closeOnce.Do(func() { close(f.done) }) }
func (f *fakeRDPortal) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func mustInputs(t *testing.T, ev core.RemoteInputEvent) []WinInput {
	t.Helper()
	inputs, err := BuildInputs(ev)
	if err != nil {
		t.Fatal(err)
	}
	return inputs
}

func authorize(t *testing.T, b *rdInputBackend) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return b.AuthorizeRemoteInput(ctx)
}

func TestRDBackendPendingUntilAuthorizedThenInjects(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3, hasStream: true, stream: 44, width: 1001, height: 501, restoreToken: "new-token"}, nil)
	portal.release = make(chan struct{})
	var saved string
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	b.loadToken = func() string { return "old-token" }
	b.saveToken = func(token string) error { saved = token; return nil }

	move := mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseMove, X: 0.5, Y: 1})
	if err := b.SendInputs(move); !errors.Is(err, ErrWaylandRemoteInputPending) {
		t.Fatalf("first SendInputs err = %v, want pending", err)
	}
	if err := b.SendInputs(move); !errors.Is(err, ErrWaylandRemoteInputPending) {
		t.Fatalf("second SendInputs err = %v, want pending", err)
	}
	if st := b.WaylandInputStatus(); st.State != WaylandInputAuthorizing {
		t.Fatalf("state = %v", st.State)
	}
	close(portal.release)
	if err := authorize(t, b); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if portal.gotToken != "old-token" || saved != "new-token" {
		t.Fatalf("restore token not round-tripped: got=%q saved=%q", portal.gotToken, saved)
	}
	st := b.WaylandInputStatus()
	if st.State != WaylandInputActive || !st.Keyboard || !st.Pointer || !st.AbsolutePointer {
		t.Fatalf("status = %+v", st)
	}
	if err := b.SendInputs(move); err != nil {
		t.Fatal(err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseDown, Button: 2, X: -1, Y: -1})); err != nil {
		t.Fatal(err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseWheel, Delta: 120})); err != nil {
		t.Fatal(err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 0x1b})); err != nil {
		t.Fatal(err)
	}
	want := []string{"abs 44 500 500", "btn 0x111 true", "axis 0 -1", "key 1 true"}
	got := portal.snapshot()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v want %v", got, want)
	}
}

func TestRDBackendDenialIsReportedAndBackedOff(t *testing.T) {
	attempts := 0
	now := time.Unix(1000, 0)
	b := newRDInputBackend(func() (rdPortal, error) {
		attempts++
		return newFakeRDPortal(rdSessionInfo{}, rdResponseError(1, "Start")), nil
	})
	b.now = func() time.Time { return now }
	if err := authorize(t, b); !errors.Is(err, ErrWaylandRemoteInputDenied) {
		t.Fatalf("authorize err = %v, want denied", err)
	}
	if st := b.WaylandInputStatus(); st.State != WaylandInputDenied {
		t.Fatalf("state = %v", st.State)
	}
	key := mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})
	if err := b.SendInputs(key); !errors.Is(err, ErrWaylandRemoteInputDenied) {
		t.Fatalf("SendInputs after denial = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("dialog re-requested during back-off: attempts=%d", attempts)
	}
	now = now.Add(rdRetryAfterDenied + time.Second)
	if err := b.SendInputs(key); !errors.Is(err, ErrWaylandRemoteInputPending) {
		t.Fatalf("SendInputs after back-off = %v, want pending", err)
	}
	waitFor(t, func() bool { return b.WaylandInputStatus().State == WaylandInputDenied })
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRDBackendPortalMissingIsUnavailable(t *testing.T) {
	b := newRDInputBackend(func() (rdPortal, error) {
		return nil, fmt.Errorf("%w: no portal", ErrWaylandRemoteInputUnavailable)
	})
	if err := authorize(t, b); !errors.Is(err, ErrWaylandRemoteInputUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})); !errors.Is(err, ErrWaylandRemoteInputUnavailable) {
		t.Fatalf("SendInputs err = %v", err)
	}
	if st := b.WaylandInputStatus(); st.State != WaylandInputUnavailable {
		t.Fatalf("state = %v", st.State)
	}
}

func TestRDBackendRevokedSessionReauthorizes(t *testing.T) {
	var portals []*fakeRDPortal
	var mu sync.Mutex
	b := newRDInputBackend(func() (rdPortal, error) {
		mu.Lock()
		defer mu.Unlock()
		p := newFakeRDPortal(rdSessionInfo{devices: 3, hasStream: true, stream: 1, width: 10, height: 10}, nil)
		portals = append(portals, p)
		return p, nil
	})
	if err := authorize(t, b); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := portals[0]
	mu.Unlock()
	first.revoke() // compositor closed the session
	waitFor(t, func() bool { return b.WaylandInputStatus().State == WaylandInputIdle })
	key := mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})
	if err := b.SendInputs(key); !errors.Is(err, ErrWaylandRemoteInputPending) {
		t.Fatalf("SendInputs after revoke = %v, want pending re-authorization", err)
	}
	waitFor(t, func() bool { return b.WaylandInputStatus().State == WaylandInputActive })
	if err := b.SendInputs(key); err != nil {
		t.Fatal(err)
	}
	if len(first.snapshot()) != 0 {
		t.Fatal("input sent to revoked session")
	}
}

func TestRDBackendNotifyFailureAfterCloseMarksClosed(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3, hasStream: true, stream: 1, width: 10, height: 10}, nil)
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	if err := authorize(t, b); err != nil {
		t.Fatal(err)
	}
	portal.mu.Lock()
	portal.notifyErr = errors.New("org.freedesktop.DBus.Error.AccessDenied: Invalid session")
	portal.mu.Unlock()
	portal.revoke()
	err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'}))
	if err == nil {
		t.Fatal("SendInputs reported success on a closed session")
	}
}

func TestRDBackendDeviceNotGranted(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: rdDeviceKeyboard}, nil)
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	if err := authorize(t, b); err != nil {
		t.Fatal(err)
	}
	err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseMove, X: 0.1, Y: 0.1}))
	if !errors.Is(err, ErrWaylandRemoteInputDeviceDenied) {
		t.Fatalf("pointer without grant err = %v", err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})); err != nil {
		t.Fatalf("keyboard err = %v", err)
	}
}

func TestRDBackendNoDevicesIsDenied(t *testing.T) {
	b := newRDInputBackend(func() (rdPortal, error) { return newFakeRDPortal(rdSessionInfo{}, nil), nil })
	if err := authorize(t, b); !errors.Is(err, ErrWaylandRemoteInputDeviceDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestRDBackendRelativeFallbackWithoutStream(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3}, nil)
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	b.extent = func() (int32, int32, bool) { return 101, 51, true }
	if err := authorize(t, b); err != nil {
		t.Fatal(err)
	}
	if st := b.WaylandInputStatus(); st.AbsolutePointer {
		t.Fatal("absolute pointer reported without stream")
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseMove, X: 0.5, Y: 0.5})); err != nil {
		t.Fatal(err)
	}
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseMove, X: 1, Y: 0})); err != nil {
		t.Fatal(err)
	}
	want := []string{"rel -404 -404", "rel 50 25", "rel 50 -25"}
	if got := portal.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("calls = %v want %v", got, want)
	}
}

func TestRDBackendRelativeFallbackWithoutExtentFails(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3}, nil)
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	if err := authorize(t, b); err != nil {
		t.Fatal(err)
	}
	err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseMove, X: 0.5, Y: 0.5}))
	if !errors.Is(err, ErrWaylandAbsolutePointerUnavailable) {
		t.Fatalf("err = %v", err)
	}
	// Buttons and wheel still work without positioning.
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputMouseWheel, Delta: -120})); err != nil {
		t.Fatal(err)
	}
}

func TestRDBackendCloseCancelsPendingAuthorization(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3}, nil)
	portal.release = make(chan struct{})
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})); !errors.Is(err, ErrWaylandRemoteInputPending) {
		t.Fatalf("err = %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		portal.mu.Lock()
		defer portal.mu.Unlock()
		return portal.closed
	})
	if err := b.SendInputs(mustInputs(t, core.RemoteInputEvent{Kind: core.InputKeyDown, VKCode: 'A'})); !errors.Is(err, ErrWaylandRemoteInputClosed) {
		t.Fatalf("after close err = %v", err)
	}
}

func TestRDBackendAuthorizeTimeout(t *testing.T) {
	portal := newFakeRDPortal(rdSessionInfo{devices: 3}, nil)
	portal.release = make(chan struct{})
	b := newRDInputBackend(func() (rdPortal, error) { return portal, nil })
	b.authorizeTimeout = 20 * time.Millisecond
	if err := authorize(t, b); !errors.Is(err, ErrWaylandRemoteInputDenied) {
		t.Fatalf("err = %v, want denied (timed out)", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
