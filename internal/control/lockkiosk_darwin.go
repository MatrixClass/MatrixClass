//go:build darwin

package control

import (
	"errors"
	"sync"
	"time"

	"github.com/ebitengine/purego/objc"
)

// NSApplicationPresentationOptions.
const (
	darwinPresentationHideDock                  uint64 = 1 << 1
	darwinPresentationHideMenuBar               uint64 = 1 << 3
	darwinPresentationDisableProcessSwitching   uint64 = 1 << 5
	darwinPresentationDisableForceQuit          uint64 = 1 << 6
	darwinPresentationDisableSessionTermination uint64 = 1 << 7
	darwinPresentationDisableHideApplication    uint64 = 1 << 8
	darwinPresentationFullScreen                uint64 = 1 << 10

	// darwinKioskOptions is a combination that satisfies every constraint
	// NSApplication enforces (HideMenuBar and the Disable* flags all require
	// HideDock); an invalid combination would raise an Objective-C exception.
	darwinKioskOptions = darwinPresentationHideDock | darwinPresentationHideMenuBar |
		darwinPresentationDisableProcessSwitching | darwinPresentationDisableForceQuit |
		darwinPresentationDisableSessionTermination | darwinPresentationDisableHideApplication
)

var errDarwinKioskFullScreen = errors.New("control: app is in native full screen; kiosk presentation options were left unchanged")

const darwinMainThreadTimeout = 3 * time.Second

// darwinKiosk applies/restores process-wide kiosk restrictions. These are
// owned by this process, so macOS drops them automatically if it exits: the
// lock can never outlive the student app.
type darwinKiosk interface {
	Enter() error
	Exit() error
}

// darwinPresentationAccess abstracts NSApp so the state machine is testable.
type darwinPresentationAccess interface {
	available() bool
	options() uint64
	setOptions(uint64)
	activate()
}

// darwinKioskPlan decides which options to set when entering kiosk mode.
// It returns ok=false when changing options would be unsafe (native full
// screen is managed by the system and NSApplication raises on many changes).
func darwinKioskPlan(current uint64) (next uint64, ok bool) {
	if current&darwinPresentationFullScreen != 0 {
		return current, false
	}
	return darwinKioskOptions, true
}

// darwinKioskRestorePlan decides what to restore on exit. done=false means
// "try again later" (full screen still active).
func darwinKioskRestorePlan(current, saved uint64) (next uint64, apply, done bool) {
	if current&darwinKioskOptions == 0 {
		return current, false, true // nothing of ours is active
	}
	if current&darwinPresentationFullScreen != 0 {
		return current, false, false
	}
	return saved &^ darwinPresentationFullScreen, true, true
}

type darwinPresentationKiosk struct {
	mu      sync.Mutex
	access  darwinPresentationAccess
	run     func(func()) error
	saved   uint64
	entered bool
	gen     uint64
	retry   time.Duration
	retries int
}

func newDarwinPresentationKiosk() *darwinPresentationKiosk {
	return &darwinPresentationKiosk{
		access:  nsAppPresentation{},
		run:     func(fn func()) error { return runOnDarwinMainThread(darwinMainThreadTimeout, fn) },
		retry:   500 * time.Millisecond,
		retries: 20,
	}
}

func (k *darwinPresentationKiosk) Enter() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.gen++ // cancel any pending restore retry
	var applied, available bool
	err := k.run(func() {
		if available = k.access.available(); !available {
			return
		}
		current := k.access.options()
		next, ok := darwinKioskPlan(current)
		k.access.activate()
		if !ok {
			return
		}
		// If our options are still applied (a restore from the previous lock
		// is pending), keep the original saved options instead.
		if current&darwinKioskOptions != darwinKioskOptions {
			k.saved = current
		}
		k.access.setOptions(next)
		applied = true
	})
	if err != nil {
		// The request may still run later on the main queue; mark as entered
		// so Exit queues a (no-op safe) restore behind it.
		k.entered = true
		return err
	}
	k.entered = applied
	if !available {
		return errDarwinNSAppNotAvailable
	}
	if !applied {
		return errDarwinKioskFullScreen
	}
	return nil
}

func (k *darwinPresentationKiosk) Exit() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.entered {
		return nil
	}
	k.entered = false
	k.gen++
	done, err := k.restoreLocked()
	if err == nil && done {
		return nil
	}
	// Either the window is still in native full screen (the UI leaves it
	// after the lock is released) or the main thread was busy. Keep retrying
	// in the background until the options can be restored safely.
	go k.retryRestore(k.gen, k.saved)
	return err
}

func (k *darwinPresentationKiosk) restoreLocked() (bool, error) {
	saved := k.saved
	var done bool
	err := k.run(func() {
		next, apply, finished := darwinKioskRestorePlan(k.access.options(), saved)
		if apply {
			k.access.setOptions(next)
		}
		done = finished
	})
	return done, err
}

func (k *darwinPresentationKiosk) retryRestore(gen, saved uint64) {
	for i := 0; i < k.retries; i++ {
		time.Sleep(k.retry)
		k.mu.Lock()
		if k.gen != gen {
			k.mu.Unlock()
			return // a new lock started; it owns the options now
		}
		k.saved = saved
		done, err := k.restoreLocked()
		k.mu.Unlock()
		if err == nil && done {
			return
		}
	}
}

// nsAppPresentation talks to [NSApplication sharedApplication]. Must be used
// on the main thread.
type nsAppPresentation struct{}

var (
	selSharedApplication       = sync.OnceValue(func() objc.SEL { return objc.RegisterName("sharedApplication") })
	selPresentationOptions     = sync.OnceValue(func() objc.SEL { return objc.RegisterName("presentationOptions") })
	selSetPresentationOptions  = sync.OnceValue(func() objc.SEL { return objc.RegisterName("setPresentationOptions:") })
	selActivateIgnoringOthers  = sync.OnceValue(func() objc.SEL { return objc.RegisterName("activateIgnoringOtherApps:") })
	nsApplicationClass         = sync.OnceValue(func() objc.ID { return objc.ID(objc.GetClass("NSApplication")) })
	errDarwinNSAppNotAvailable = errors.New("control: NSApplication is not available (AppKit not loaded)")
)

func (p nsAppPresentation) available() bool { return p.app() != 0 }

func (nsAppPresentation) app() objc.ID {
	cls := nsApplicationClass()
	if cls == 0 {
		return 0
	}
	return cls.Send(selSharedApplication())
}

func (p nsAppPresentation) options() uint64 {
	app := p.app()
	if app == 0 {
		return 0
	}
	return objc.Send[uint64](app, selPresentationOptions())
}

func (p nsAppPresentation) setOptions(options uint64) {
	if app := p.app(); app != 0 {
		app.Send(selSetPresentationOptions(), options)
	}
}

func (p nsAppPresentation) activate() {
	if app := p.app(); app != 0 {
		app.Send(selActivateIgnoringOthers(), true)
	}
}
