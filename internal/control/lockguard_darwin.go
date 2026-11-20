//go:build darwin

package control

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The shortcut guard installs a session CGEventTap while the classroom lock
// is active and drops system/app shortcuts (Cmd+Q, Cmd+Tab, Cmd+Space,
// Ctrl+arrows, Mission Control, ...). Plain typing still reaches the lock
// overlay so the emergency unlock password can be entered. Creating an
// active (filtering) tap requires the Accessibility permission; without it
// the guard reports an error and the lock continues with overlay + kiosk
// presentation options only.

var errDarwinShortcutGuardUnavailable = errors.New("control: macOS shortcut guard unavailable (Accessibility permission required)")

const (
	darwinEventKeyDown              = 10
	darwinEventKeyUp                = 11
	darwinEventTapDisabledByTimeout = 0xFFFFFFFE
	darwinEventTapDisabledByInput   = 0xFFFFFFFF
	darwinSessionEventTap           = 1
	darwinHeadInsertEventTap        = 0
	darwinEventTapOptionDefault     = 0
	darwinKeyboardEventKeycode      = 9

	darwinFlagShift   uint64 = 0x00020000
	darwinFlagControl uint64 = 0x00040000
	darwinFlagOption  uint64 = 0x00080000
	darwinFlagCommand uint64 = 0x00100000

	darwinKeyMissionControl = 160
	darwinKeyLaunchpad      = 131
	darwinKeyF11ShowDesktop = 103
)

type darwinShortcutGuard interface {
	Start() error
	Stop()
}

// darwinShouldBlockLockedKey reports whether a key event must be swallowed
// while locked: anything chorded with Command or Control (app/system
// shortcuts, Spaces switching, Force Quit), plus dedicated Mission Control,
// Launchpad and Show Desktop keys.
func darwinShouldBlockLockedKey(keyCode uint16, flags uint64) bool {
	if flags&(darwinFlagCommand|darwinFlagControl) != 0 {
		return true
	}
	switch keyCode {
	case darwinKeyMissionControl, darwinKeyLaunchpad, darwinKeyF11ShowDesktop:
		return true
	}
	return false
}

type darwinEventTapAPI struct {
	tapCreate      func(tap, place, options uint32, mask uint64, callback, userInfo uintptr) uintptr
	tapEnable      func(tap uintptr, enable bool)
	getIntField    func(event uintptr, field uint32) int64
	getFlags       func(event uintptr) uint64
	createSource   func(allocator, port uintptr, order int) uintptr
	runLoopCurrent func() uintptr
	addSource      func(loop, source, mode uintptr)
	removeSource   func(loop, source, mode uintptr)
	runInMode      func(mode uintptr, seconds float64, returnAfterSourceHandled bool) int32
	stopLoop       func(loop uintptr)
	invalidate     func(port uintptr)
	release        func(uintptr)
	defaultMode    uintptr
	callback       uintptr
}

// darwinActiveTap is read by the (single, process-wide) tap callback.
var darwinActiveTap atomic.Uintptr

var loadDarwinEventTapAPI = sync.OnceValues(func() (*darwinEventTapAPI, error) {
	graphics, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", darwinRTLDNow)
	if err != nil {
		return nil, errors.Join(errDarwinShortcutGuardUnavailable, err)
	}
	foundation, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", darwinRTLDNow)
	if err != nil {
		_ = purego.Dlclose(graphics)
		return nil, errors.Join(errDarwinShortcutGuardUnavailable, err)
	}
	keepLibraries := false
	defer func() {
		if !keepLibraries {
			_ = purego.Dlclose(foundation)
			_ = purego.Dlclose(graphics)
		}
	}()
	api := &darwinEventTapAPI{}
	bind := []struct {
		handle uintptr
		name   string
		fn     any
	}{
		{graphics, "CGEventTapCreate", &api.tapCreate},
		{graphics, "CGEventTapEnable", &api.tapEnable},
		{graphics, "CGEventGetIntegerValueField", &api.getIntField},
		{graphics, "CGEventGetFlags", &api.getFlags},
		{foundation, "CFMachPortCreateRunLoopSource", &api.createSource},
		{foundation, "CFRunLoopGetCurrent", &api.runLoopCurrent},
		{foundation, "CFRunLoopAddSource", &api.addSource},
		{foundation, "CFRunLoopRemoveSource", &api.removeSource},
		{foundation, "CFRunLoopRunInMode", &api.runInMode},
		{foundation, "CFRunLoopStop", &api.stopLoop},
		{foundation, "CFMachPortInvalidate", &api.invalidate},
		{foundation, "CFRelease", &api.release},
	}
	for _, b := range bind {
		address, err := purego.Dlsym(b.handle, b.name)
		if err != nil || address == 0 {
			return nil, errors.Join(errDarwinShortcutGuardUnavailable, errors.New("missing symbol "+b.name))
		}
		purego.RegisterFunc(b.fn, address)
	}
	modeSymbol, err := purego.Dlsym(foundation, "kCFRunLoopDefaultMode")
	if err != nil || modeSymbol == 0 {
		return nil, errors.Join(errDarwinShortcutGuardUnavailable, errors.New("missing kCFRunLoopDefaultMode"))
	}
	api.defaultMode = *(*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&modeSymbol)))
	api.callback = purego.NewCallback(func(_ uintptr, eventType uintptr, event uintptr, _ uintptr) uintptr {
		switch uint32(eventType) {
		case darwinEventTapDisabledByTimeout, darwinEventTapDisabledByInput:
			// macOS disables slow taps; turn it back on while locked.
			if tap := darwinActiveTap.Load(); tap != 0 {
				api.tapEnable(tap, true)
			}
			return event
		case darwinEventKeyDown, darwinEventKeyUp:
			if darwinActiveTap.Load() == 0 {
				return event
			}
			keyCode := uint16(api.getIntField(event, darwinKeyboardEventKeycode))
			if darwinShouldBlockLockedKey(keyCode, api.getFlags(event)) {
				return 0 // swallow
			}
		}
		return event
	})
	keepLibraries = true
	return api, nil
})

type darwinEventTapGuard struct {
	mu      sync.Mutex
	running bool
	stop    atomic.Bool
	loop    atomic.Uintptr
	done    chan struct{}
}

func newDarwinEventTapGuard() *darwinEventTapGuard { return &darwinEventTapGuard{} }

func (g *darwinEventTapGuard) Start() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running {
		return nil
	}
	api, err := loadDarwinEventTapAPI()
	if err != nil {
		return err
	}
	ready := make(chan error, 1)
	g.done = make(chan struct{})
	g.stop.Store(false)
	go g.run(api, ready, g.done)
	if err := <-ready; err != nil {
		return err
	}
	g.running = true
	return nil
}

func (g *darwinEventTapGuard) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.running {
		return
	}
	g.running = false
	g.stop.Store(true)
	if api, err := loadDarwinEventTapAPI(); err == nil {
		if loop := g.loop.Load(); loop != 0 {
			api.stopLoop(loop)
		}
	}
	<-g.done
}

func (g *darwinEventTapGuard) run(api *darwinEventTapAPI, ready chan<- error, done chan struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(done)

	mask := uint64(1)<<darwinEventKeyDown | uint64(1)<<darwinEventKeyUp
	tap := api.tapCreate(darwinSessionEventTap, darwinHeadInsertEventTap, darwinEventTapOptionDefault, mask, api.callback, 0)
	if tap == 0 {
		ready <- errDarwinShortcutGuardUnavailable
		return
	}
	source := api.createSource(0, tap, 0)
	if source == 0 {
		api.invalidate(tap)
		api.release(tap)
		ready <- errDarwinShortcutGuardUnavailable
		return
	}
	loop := api.runLoopCurrent()
	api.addSource(loop, source, api.defaultMode)
	api.tapEnable(tap, true)
	darwinActiveTap.Store(tap)
	g.loop.Store(loop)
	ready <- nil

	// Run in short slices so a Stop that races with startup is never missed.
	for !g.stop.Load() {
		api.runInMode(api.defaultMode, 0.25, false)
	}

	darwinActiveTap.CompareAndSwap(tap, 0)
	g.loop.Store(0)
	api.tapEnable(tap, false)
	api.removeSource(loop, source, api.defaultMode)
	api.invalidate(tap)
	api.release(source)
	api.release(tap)
}
