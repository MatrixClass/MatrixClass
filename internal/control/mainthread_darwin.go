//go:build darwin

package control

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ebitengine/purego"
)

// AppKit state (NSApplication presentation options, activation) must only be
// touched from the main thread. The UI framework runs the Cocoa event loop on
// the main thread, which drains the main GCD queue, so work is submitted with
// dispatch_async_f and awaited with a timeout.

var errDarwinMainThreadUnavailable = errors.New("control: macOS main thread did not run the request (is the Cocoa event loop running?)")

type darwinMainDispatcher struct {
	mainQueue uintptr
	asyncF    func(queue, context, work uintptr)
	isMain    func() int32
	callback  uintptr

	mu      sync.Mutex
	nextID  uintptr
	pending map[uintptr]func()
}

var darwinMainThread = sync.OnceValues(func() (*darwinMainDispatcher, error) {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", darwinRTLDNow|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, errors.Join(errDarwinMainThreadUnavailable, err)
	}
	queue, err := purego.Dlsym(lib, "_dispatch_main_q")
	if err != nil {
		return nil, errors.Join(errDarwinMainThreadUnavailable, err)
	}
	asyncF, err := purego.Dlsym(lib, "dispatch_async_f")
	if err != nil {
		return nil, errors.Join(errDarwinMainThreadUnavailable, err)
	}
	isMain, err := purego.Dlsym(lib, "pthread_main_np")
	if err != nil {
		return nil, errors.Join(errDarwinMainThreadUnavailable, err)
	}
	d := &darwinMainDispatcher{mainQueue: queue, pending: make(map[uintptr]func())}
	purego.RegisterFunc(&d.asyncF, asyncF)
	purego.RegisterFunc(&d.isMain, isMain)
	// A single callback serves every request; callbacks are a finite resource.
	d.callback = purego.NewCallback(func(context uintptr) {
		d.mu.Lock()
		fn := d.pending[context]
		delete(d.pending, context)
		d.mu.Unlock()
		if fn != nil {
			fn()
		}
	})
	return d, nil
})

// runOnDarwinMainThread runs fn on the main thread and waits up to timeout.
// When the wait times out fn may still run later; callers must tolerate that
// (the main queue is serial, so later requests still run after it).
func runOnDarwinMainThread(timeout time.Duration, fn func()) error {
	d, err := darwinMainThread()
	if err != nil {
		return err
	}
	if d.isMain() != 0 {
		fn()
		return nil
	}
	done := make(chan struct{})
	var ran atomic.Bool
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	d.pending[id] = func() {
		ran.Store(true)
		fn()
		close(done)
	}
	d.mu.Unlock()
	d.asyncF(d.mainQueue, id, d.callback)
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		if ran.Load() {
			// Started but slow; wait for completion to keep ordering simple.
			<-done
			return nil
		}
		return errDarwinMainThreadUnavailable
	}
}
