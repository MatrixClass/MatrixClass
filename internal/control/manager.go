package control

import (
	"context"
	"errors"
	"nexlab/internal/core"
	"sync"
)

var _ core.ControlEngine = (*Manager)(nil)

type ManagerOptions struct {
	InputBackend   InputBackend
	Lock           LockOptions
	Policy         PolicyOptions
	Power          PowerOptions
	ProxyAddr      string
	EnforceOnApply bool
}

type Manager struct {
	mu             sync.Mutex
	input          *InputInjector
	lock           *ScreenLockController
	policy         *PolicyEnforcer
	power          *PowerController
	proxyAddr      string
	enforceOnApply bool
	lockedByPolicy bool
}

func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		input:          NewInputInjector(opts.InputBackend),
		lock:           NewScreenLockController(opts.Lock),
		policy:         NewPolicyEnforcer(opts.Policy),
		power:          NewPowerController(opts.Power),
		proxyAddr:      opts.ProxyAddr,
		enforceOnApply: opts.EnforceOnApply,
	}
}

func (m *Manager) InjectInput(ev core.RemoteInputEvent) error {
	return m.input.Inject(ev)
}

func (m *Manager) SetScreenLock(locked bool, message string) error {
	m.mu.Lock()
	m.lockedByPolicy = false
	m.mu.Unlock()
	return m.lock.SetLocked(locked, message)
}

func (m *Manager) ApplyPolicy(rule core.PolicyRule) error {
	if err := m.policy.ApplyRule(rule); err != nil {
		return err
	}

	m.mu.Lock()
	wasLockedByPolicy := m.lockedByPolicy
	if rule.LockScreen {
		m.lockedByPolicy = true
	} else if wasLockedByPolicy {
		m.lockedByPolicy = false
	}
	m.mu.Unlock()

	if rule.LockScreen {
		if err := m.lock.SetLocked(true, rule.LockMessage); err != nil {
			return err
		}
	} else if wasLockedByPolicy {
		if err := m.lock.SetLocked(false, ""); err != nil {
			return err
		}
	}

	if m.enforceOnApply {
		if _, err := m.policy.EnforceProcessesOnce(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) ExecutePower(action core.PowerAction, macAddr string) error {
	return m.power.Execute(action, macAddr)
}

func (m *Manager) Start(ctx context.Context) (string, error) {
	m.policy.StartMonitor(ctx)
	if m.proxyAddr == "" {
		return "", nil
	}
	return m.policy.StartDomainProxy(m.proxyAddr)
}

func (m *Manager) Close() error {
	return errors.Join(m.lock.Close(), m.policy.Close())
}

func (m *Manager) Input() *InputInjector {
	return m.input
}

func (m *Manager) Lock() *ScreenLockController {
	return m.lock
}

func (m *Manager) Policy() *PolicyEnforcer {
	return m.policy
}

func (m *Manager) Power() *PowerController {
	return m.power
}

func (m *Manager) EmergencyUnlock(password string) (bool, error) {
	ok, err := m.lock.EmergencyUnlock(password)
	if ok && err == nil {
		m.mu.Lock()
		m.lockedByPolicy = false
		m.mu.Unlock()
	}
	return ok, err
}

func (m *Manager) LockState() LockState {
	return m.lock.State()
}

func (m *Manager) NotifyTeacherHeartbeat() {
	m.lock.NotifyTeacherHeartbeat()
}

func (m *Manager) EnforceProcessesOnce() ([]ProcessInfo, error) {
	return m.policy.EnforceProcessesOnce()
}

func (m *Manager) StartDomainProxy(addr string) (string, error) {
	return m.policy.StartDomainProxy(addr)
}

func (m *Manager) StopDomainProxy(ctx context.Context) error {
	return m.policy.StopDomainProxy(ctx)
}
