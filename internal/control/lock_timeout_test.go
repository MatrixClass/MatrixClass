package control

import (
	"sync"
	"testing"
	"time"
)

func TestScreenLockDisconnectTimeoutAutoUnlocksAndNotifies(t *testing.T) {
	backend := &mockLockBackend{}
	states := make(chan LockState, 4)
	ctrl := NewScreenLockController(LockOptions{
		Backend:           backend,
		DisconnectTimeout: 50 * time.Millisecond,
		OnStateChange:     func(s LockState) { states <- s },
	})
	defer ctrl.Close()
	if err := ctrl.SetLocked(true, "locked"); err != nil {
		t.Fatal(err)
	}
	if s := <-states; !s.Locked {
		t.Fatalf("first state should be locked: %+v", s)
	}
	select {
	case s := <-states:
		if s.Locked || s.UnlockReason != UnlockReasonDisconnectTimeout {
			t.Fatalf("unexpected auto-unlock state: %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lock was not released after disconnect timeout")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.enabled || backend.disables != 1 {
		t.Fatalf("backend not disabled exactly once: enabled=%v disables=%d", backend.enabled, backend.disables)
	}
}

func TestScreenLockDisconnectTimeoutDefaultsAndCanBeDisabled(t *testing.T) {
	ctrl := NewScreenLockController(LockOptions{Backend: &mockLockBackend{}})
	defer ctrl.Close()
	ctrl.mu.Lock()
	got := ctrl.disconnectTimeout
	ctrl.mu.Unlock()
	if got != DefaultLockDisconnectTimeout {
		t.Fatalf("default disconnect timeout = %v, want %v", got, DefaultLockDisconnectTimeout)
	}

	disabled := NewScreenLockController(LockOptions{Backend: &mockLockBackend{}, DisconnectTimeout: -1})
	defer disabled.Close()
	if err := disabled.SetLocked(true, "x"); err != nil {
		t.Fatal(err)
	}
	disabled.mu.Lock()
	timer := disabled.timer
	disabled.mu.Unlock()
	if timer != nil {
		t.Fatal("negative DisconnectTimeout must not arm a timer")
	}
}

func TestScreenLockDisconnectTimerRearmsWhenClockLags(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	states := make(chan LockState, 4)
	ctrl := NewScreenLockController(LockOptions{
		Backend:           &mockLockBackend{},
		DisconnectTimeout: 30 * time.Millisecond,
		Now:               clock,
		OnStateChange:     func(s LockState) { states <- s },
	})
	defer ctrl.Close()
	if err := ctrl.SetLocked(true, "x"); err != nil {
		t.Fatal(err)
	}
	<-states
	// The injected clock has not advanced, so the first timer fire must re-arm
	// rather than drop the auto-unlock.
	time.Sleep(80 * time.Millisecond)
	if !ctrl.State().Locked {
		t.Fatal("unlocked although the injected clock did not advance")
	}
	mu.Lock()
	now = now.Add(time.Second)
	mu.Unlock()
	select {
	case s := <-states:
		if s.Locked || s.UnlockReason != UnlockReasonDisconnectTimeout {
			t.Fatalf("unexpected state: %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("re-armed disconnect timer never unlocked")
	}
}
