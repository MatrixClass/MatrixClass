package control

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"sync"
	"time"
)

var (
	ErrInvalidEmergencyPassword = errors.New("control: invalid emergency unlock password")
)

type LockState struct {
	Locked          bool      `json:"locked"`
	Message         string    `json:"message"`
	LockedAt        time.Time `json:"locked_at,omitempty"`
	LastHeartbeat   time.Time `json:"last_heartbeat,omitempty"`
	UnlockReason    string    `json:"unlock_reason,omitempty"`
	EmergencyActive bool      `json:"emergency_active"`
}

type LockBackend interface {
	EnableLock(message string) error
	DisableLock() error
	UpdateMessage(message string) error
}

type OverlayHooks struct {
	OnShow   func(message string)
	OnHide   func()
	OnUpdate func(message string)
}

// DefaultLockDisconnectTimeout is applied when LockOptions.DisconnectTimeout is
// zero so a locked student cannot stay locked forever after the teacher
// disappears. The teacher pings every few seconds, so this leaves a wide margin.
// Pass a negative DisconnectTimeout to disable the auto-unlock explicitly.
const DefaultLockDisconnectTimeout = 2 * time.Minute

// UnlockReasonDisconnectTimeout is reported in LockState.UnlockReason when the
// lock is released because no teacher heartbeat arrived within the timeout.
const UnlockReasonDisconnectTimeout = "disconnect_timeout"

type LockOptions struct {
	Backend           LockBackend
	Overlays          OverlayHooks
	EmergencyPassHash *[32]byte
	EmergencyPassword string
	DisconnectTimeout time.Duration
	OnStateChange     func(LockState)
	Now               func() time.Time
}

type ScreenLockController struct {
	mu                sync.Mutex
	backend           LockBackend
	locked            bool
	message           string
	lockedAt          time.Time
	lastHeartbeat     time.Time
	unlockReason      string
	emergencyPassHash [32]byte
	hasEmergencyPass  bool
	disconnectTimeout time.Duration
	timer             *time.Timer
	timerGen          uint64
	onStateChange     func(LockState)
	nowFn             func() time.Time
	closed            bool
}

func HashPasswordSHA256(password string) [32]byte {
	return sha256.Sum256([]byte(password))
}

func NewScreenLockController(opts LockOptions) *ScreenLockController {
	backend := opts.Backend
	if backend == nil {
		backend = newPlatformLockBackend(opts.Overlays)
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	disconnectTimeout := opts.DisconnectTimeout
	if disconnectTimeout == 0 {
		disconnectTimeout = DefaultLockDisconnectTimeout
	}
	c := &ScreenLockController{
		backend:           backend,
		disconnectTimeout: disconnectTimeout,
		onStateChange:     opts.OnStateChange,
		nowFn:             nowFn,
		lastHeartbeat:     nowFn(),
	}
	if opts.EmergencyPassHash != nil {
		c.emergencyPassHash = *opts.EmergencyPassHash
		c.hasEmergencyPass = true
	} else if opts.EmergencyPassword != "" {
		c.emergencyPassHash = HashPasswordSHA256(opts.EmergencyPassword)
		c.hasEmergencyPass = true
	}
	return c
}

func (c *ScreenLockController) SetEmergencyPassword(password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if password == "" {
		c.emergencyPassHash = [32]byte{}
		c.hasEmergencyPass = false
		return
	}
	c.emergencyPassHash = HashPasswordSHA256(password)
	c.hasEmergencyPass = true
}

func (c *ScreenLockController) SetEmergencyPasswordHash(hash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.emergencyPassHash = hash
	c.hasEmergencyPass = true
}

func (c *ScreenLockController) VerifyEmergencyPassword(password string) bool {
	c.mu.Lock()
	hasPass := c.hasEmergencyPass
	expected := c.emergencyPassHash
	c.mu.Unlock()

	if !hasPass || password == "" {
		return false
	}
	actual := HashPasswordSHA256(password)
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}

func (c *ScreenLockController) SetDisconnectTimeout(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnectTimeout = d
	if c.locked {
		c.resetTimerLocked()
	}
}

func (c *ScreenLockController) SetLocked(locked bool, message string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	now := c.nowFn()
	if locked {
		wasLocked := c.locked
		c.locked = true
		c.message = message
		c.unlockReason = ""
		if !wasLocked {
			c.lockedAt = now
		}
		c.lastHeartbeat = now
		c.resetTimerLocked()
		backend := c.backend
		cb := c.onStateChange
		state := c.snapshotLocked()
		c.mu.Unlock()

		var err error
		if wasLocked {
			err = backend.UpdateMessage(message)
		} else {
			err = backend.EnableLock(message)
		}
		if err != nil {
			if !wasLocked {
				c.mu.Lock()
				c.locked = false
				c.message = ""
				c.stopTimerLocked()
				c.mu.Unlock()
			}
			return err
		}
		if cb != nil {
			cb(state)
		}
		return nil
	}

	return c.unlockWithReason("teacher_unlock")
}

func (c *ScreenLockController) EmergencyUnlock(password string) (bool, error) {
	if !c.VerifyEmergencyPassword(password) {
		return false, ErrInvalidEmergencyPassword
	}
	c.mu.Lock()
	if !c.locked {
		c.mu.Unlock()
		return true, nil
	}
	err := c.unlockWithReason("emergency_password")
	return err == nil, err
}

// unlockWithReason must be called with c.mu held; it releases c.mu before returning.
func (c *ScreenLockController) unlockWithReason(reason string) error {
	wasLocked := c.locked
	c.locked = false
	c.message = ""
	c.unlockReason = reason
	c.stopTimerLocked()
	backend := c.backend
	cb := c.onStateChange
	state := c.snapshotLocked()
	c.mu.Unlock()

	if wasLocked {
		if err := backend.DisableLock(); err != nil {
			return err
		}
	}
	if cb != nil && wasLocked {
		cb(state)
	}
	return nil
}

func (c *ScreenLockController) NotifyTeacherHeartbeat() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastHeartbeat = c.nowFn()
	if c.locked {
		c.resetTimerLocked()
	}
}

func (c *ScreenLockController) CheckDisconnectTimeout() bool {
	c.mu.Lock()
	if !c.locked || c.disconnectTimeout <= 0 {
		c.mu.Unlock()
		return false
	}
	if c.nowFn().Sub(c.lastHeartbeat) < c.disconnectTimeout {
		c.mu.Unlock()
		return false
	}
	_ = c.unlockWithReason(UnlockReasonDisconnectTimeout)
	return true
}

// SetOnStateChange replaces the state-change callback (invoked after lock,
// teacher unlock, emergency unlock and disconnect-timeout unlock).
func (c *ScreenLockController) SetOnStateChange(cb func(LockState)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onStateChange = cb
}

func (c *ScreenLockController) State() LockState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *ScreenLockController) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	wasLocked := c.locked
	c.locked = false
	c.stopTimerLocked()
	backend := c.backend
	c.mu.Unlock()

	if wasLocked {
		return backend.DisableLock()
	}
	return nil
}

func (c *ScreenLockController) snapshotLocked() LockState {
	return LockState{
		Locked:          c.locked,
		Message:         c.message,
		LockedAt:        c.lockedAt,
		LastHeartbeat:   c.lastHeartbeat,
		UnlockReason:    c.unlockReason,
		EmergencyActive: c.hasEmergencyPass,
	}
}

func (c *ScreenLockController) resetTimerLocked() {
	c.stopTimerLocked()
	if c.disconnectTimeout <= 0 || c.closed {
		return
	}
	c.armTimerLocked(c.disconnectTimeout, c.timerGen)
}

func (c *ScreenLockController) armTimerLocked(after time.Duration, gen uint64) {
	c.timer = time.AfterFunc(after, func() {
		c.onDisconnectTimer(gen)
	})
}

// onDisconnectTimer unlocks when the heartbeat is stale, or re-arms the timer for
// the remaining time (e.g. when an injected clock lags real time) so the
// auto-unlock can never be silently dropped while locked.
func (c *ScreenLockController) onDisconnectTimer(gen uint64) {
	c.mu.Lock()
	if gen != c.timerGen || !c.locked || c.closed || c.disconnectTimeout <= 0 {
		c.mu.Unlock()
		return
	}
	remaining := c.disconnectTimeout - c.nowFn().Sub(c.lastHeartbeat)
	if remaining > 0 {
		if remaining < 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		c.armTimerLocked(remaining, gen)
		c.mu.Unlock()
		return
	}
	_ = c.unlockWithReason(UnlockReasonDisconnectTimeout)
}

func (c *ScreenLockController) stopTimerLocked() {
	c.timerGen++
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}
