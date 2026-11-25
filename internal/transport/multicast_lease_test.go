package transport

import (
	"testing"
	"time"
)

func TestMulticastConfirmationLeaseExpires(t *testing.T) {
	m := NewManager()
	defer m.Close()
	sess := &SmuxSession{}
	m.mu.Lock()
	m.sessions["s1"] = sess
	m.mcastConfirmed[sess] = time.Now()
	fresh := len(m.unconfirmedSessionsLocked())
	m.mcastConfirmed[sess] = time.Now().Add(-mcastAckLease - time.Second)
	stale := len(m.unconfirmedSessionsLocked())
	delete(m.sessions, "s1")
	m.mu.Unlock()
	if fresh != 0 {
		t.Fatalf("fresh confirmation should suppress fallback, got %d unconfirmed", fresh)
	}
	if stale != 1 {
		t.Fatalf("expired confirmation should resume fallback, got %d unconfirmed", stale)
	}
}
