//go:build linux

package control

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/ebitengine/purego"
)

var ErrInputUnsupportedPlatform = errors.New("control: X11 XTest input is unavailable")

const (
	linuxInputMotion       = "motion"
	linuxInputButton       = "button"
	linuxInputMotionButton = "motion_button"
	linuxInputKey          = "key"
	// linuxInputClick is a press immediately followed by a release (wheel notches).
	linuxInputClick = "click"
)

type linuxInputEvent struct {
	kind   string
	x      int
	y      int
	button uint32
	down   bool
	keysym uint64
}

type linuxInputBackend struct {
	mu             sync.Mutex
	handle         uintptr
	xtstHandle     uintptr
	display        uintptr
	screen         int32
	width          int32
	height         int32
	close          func(uintptr) int32
	flush          func(uintptr) int32
	motion         func(uintptr, int32, int32, int32, uint64) int32
	button         func(uintptr, uint32, int32, uint64) int32
	key            func(uintptr, uint32, int32, uint64) int32
	closeLibraries func(uintptr) error
	keycode        func(uintptr, uint64) byte
}

// linuxWaylandXTestEnv opts into injecting through XWayland's XTest when the
// Wayland portal is unavailable. XTest under XWayland only reaches X11
// clients (native Wayland windows ignore it), so it is never used silently.
const linuxWaylandXTestEnv = "MATRIXCLASS_WAYLAND_XTEST"

func newPlatformInputBackend() InputBackend {
	if linuxSessionIsWayland(os.Getenv) {
		return newLinuxWaylandInputBackend(os.Getenv(linuxWaylandXTestEnv) == "1")
	}
	backend, err := newLinuxInputBackend()
	if err != nil {
		return &linuxUnavailableInputBackend{err: err}
	}
	return backend
}

// linuxWaylandInputBackend injects through xdg-desktop-portal RemoteDesktop
// and, only when explicitly enabled, falls back to XWayland XTest if the
// portal is missing.
type linuxWaylandInputBackend struct {
	*rdInputBackend
	allowXTest bool
	newXTest   func() (InputBackend, error)

	mu       sync.Mutex
	xtest    InputBackend
	xtestErr error
}

func newLinuxWaylandInputBackend(allowXTest bool) *linuxWaylandInputBackend {
	return &linuxWaylandInputBackend{
		rdInputBackend: newLinuxPortalInputBackend(),
		allowXTest:     allowXTest,
		newXTest: func() (InputBackend, error) {
			backend, err := newLinuxInputBackend()
			if err != nil {
				return nil, err
			}
			return backend, nil
		},
	}
}

func (b *linuxWaylandInputBackend) SendInputs(inputs []WinInput) error {
	err := b.rdInputBackend.SendInputs(inputs)
	if err == nil || !b.allowXTest || !errors.Is(err, ErrWaylandRemoteInputUnavailable) {
		return err
	}
	b.mu.Lock()
	if b.xtest == nil && b.xtestErr == nil && b.newXTest != nil {
		b.xtest, b.xtestErr = b.newXTest()
	}
	xtest, xtestErr := b.xtest, b.xtestErr
	b.mu.Unlock()
	if xtest == nil {
		return fmt.Errorf("%w; XWayland XTest fallback failed: %v", err, xtestErr)
	}
	return xtest.SendInputs(inputs)
}

func (b *linuxWaylandInputBackend) Close() error {
	err := b.rdInputBackend.Close()
	b.mu.Lock()
	xtest := b.xtest
	b.xtest = nil
	b.mu.Unlock()
	if closer, ok := xtest.(interface{ Close() error }); ok {
		err = errors.Join(err, closer.Close())
	}
	return err
}

var (
	linuxX11SizeOnce             sync.Once
	linuxX11SizeW, linuxX11SizeH int32
	linuxX11SizeOK               bool
)

// linuxX11ScreenSize returns the (X)Wayland root window size, used as the
// coordinate space for relative pointer motion when the portal provides no
// screen-cast stream. The value is probed once; only libX11 is required.
func linuxX11ScreenSize() (int32, int32, bool) {
	linuxX11SizeOnce.Do(func() {
		if os.Getenv("DISPLAY") == "" {
			return
		}
		lib, err := purego.Dlopen("libX11.so.6", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			return
		}
		defer func() { _ = purego.Dlclose(lib) }()
		var open func(*byte) uintptr
		var closeDisplay func(uintptr) int32
		var defaultScreen func(uintptr) int32
		var displayWidth, displayHeight func(uintptr, int32) int32
		for name, fn := range map[string]any{"XOpenDisplay": &open, "XCloseDisplay": &closeDisplay, "XDefaultScreen": &defaultScreen, "XDisplayWidth": &displayWidth, "XDisplayHeight": &displayHeight} {
			addr, err := purego.Dlsym(lib, name)
			if err != nil || addr == 0 {
				return
			}
			purego.RegisterFunc(fn, addr)
		}
		display := open(nil)
		if display == 0 {
			return
		}
		screen := defaultScreen(display)
		linuxX11SizeW, linuxX11SizeH = displayWidth(display, screen), displayHeight(display, screen)
		_ = closeDisplay(display)
		linuxX11SizeOK = linuxX11SizeW > 0 && linuxX11SizeH > 0
	})
	return linuxX11SizeW, linuxX11SizeH, linuxX11SizeOK
}

func newLinuxInputBackend() (*linuxInputBackend, error) {
	if runtime.GOOS != "linux" || os.Getenv("DISPLAY") == "" {
		return nil, ErrInputUnsupportedPlatform
	}
	x11, err := purego.Dlopen("libX11.so.6", 2)
	if err != nil {
		return nil, fmt.Errorf("control: load libX11: %w", err)
	}
	xtst, err := purego.Dlopen("libXtst.so.6", 2)
	if err != nil {
		_ = releaseLinuxInputLibraries(x11, 0, func(handle uintptr) error { return purego.Dlclose(handle) })
		return nil, fmt.Errorf("control: load libXtst: %w", err)
	}
	lookupX11 := func(name string) uintptr { addr, _ := purego.Dlsym(x11, name); return addr }
	lookupXtst := func(name string) uintptr { addr, _ := purego.Dlsym(xtst, name); return addr }
	if lookupX11("XOpenDisplay") == 0 || lookupX11("XCloseDisplay") == 0 || lookupX11("XDefaultScreen") == 0 || lookupX11("XDisplayWidth") == 0 || lookupX11("XDisplayHeight") == 0 || lookupX11("XFlush") == 0 || lookupX11("XKeysymToKeycode") == 0 || lookupXtst("XTestFakeMotionEvent") == 0 || lookupXtst("XTestFakeButtonEvent") == 0 || lookupXtst("XTestFakeKeyEvent") == 0 {
		_ = releaseLinuxInputLibraries(x11, xtst, func(handle uintptr) error { return purego.Dlclose(handle) })
		return nil, ErrInputUnsupportedPlatform
	}
	var open func(*byte) uintptr
	var closeDisplay func(uintptr) int32
	var defaultScreen func(uintptr) int32
	var displayWidth func(uintptr, int32) int32
	var displayHeight func(uintptr, int32) int32
	var flush func(uintptr) int32
	var keysymToKeycode func(uintptr, uint64) byte
	var fakeMotion func(uintptr, int32, int32, int32, uint64) int32
	var fakeButton func(uintptr, uint32, int32, uint64) int32
	var fakeKey func(uintptr, uint32, int32, uint64) int32
	purego.RegisterFunc(&open, lookupX11("XOpenDisplay"))
	purego.RegisterFunc(&closeDisplay, lookupX11("XCloseDisplay"))
	purego.RegisterFunc(&defaultScreen, lookupX11("XDefaultScreen"))
	purego.RegisterFunc(&displayWidth, lookupX11("XDisplayWidth"))
	purego.RegisterFunc(&displayHeight, lookupX11("XDisplayHeight"))
	purego.RegisterFunc(&flush, lookupX11("XFlush"))
	purego.RegisterFunc(&keysymToKeycode, lookupX11("XKeysymToKeycode"))
	purego.RegisterFunc(&fakeMotion, lookupXtst("XTestFakeMotionEvent"))
	purego.RegisterFunc(&fakeButton, lookupXtst("XTestFakeButtonEvent"))
	purego.RegisterFunc(&fakeKey, lookupXtst("XTestFakeKeyEvent"))
	display := open(nil)
	if display == 0 {
		_ = releaseLinuxInputLibraries(x11, xtst, func(handle uintptr) error { return purego.Dlclose(handle) })
		return nil, ErrInputUnsupportedPlatform
	}
	screen := defaultScreen(display)
	return &linuxInputBackend{handle: x11, xtstHandle: xtst, display: display, screen: screen, width: displayWidth(display, screen), height: displayHeight(display, screen), close: closeDisplay, flush: flush, motion: fakeMotion, button: fakeButton, key: fakeKey, keycode: keysymToKeycode}, nil
}

func (b *linuxInputBackend) SendInputs(inputs []WinInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.display == 0 {
		return ErrInputUnsupportedPlatform
	}
	for _, input := range inputs {
		action, err := linuxInputAction(input, int(b.width), int(b.height))
		if err != nil {
			return err
		}
		var result int32
		switch action.kind {
		case linuxInputMotion, linuxInputMotionButton:
			result = b.motion(b.display, b.screen, int32(action.x), int32(action.y), 0)
			if result == 0 {
				return errors.New("control: XTestFakeMotionEvent failed")
			}
			if action.kind == linuxInputMotionButton {
				result = b.button(b.display, action.button, boolToInt32(action.down), 0)
			}
		case linuxInputButton:
			result = b.button(b.display, action.button, boolToInt32(action.down), 0)
		case linuxInputClick:
			// X11 wheel buttons must be released, otherwise the server
			// considers button 4/5 held and subsequent scrolls are dropped.
			result = b.button(b.display, action.button, 1, 0)
			if result != 0 {
				result = b.button(b.display, action.button, 0, 0)
			}
		case linuxInputKey:
			keycode := b.keycode(b.display, action.keysym)
			if keycode == 0 {
				return fmt.Errorf("control: no X11 keycode for keysym %#x", action.keysym)
			}
			result = b.key(b.display, uint32(keycode), boolToInt32(action.down), 0)
		}
		if result == 0 {
			return errors.New("control: XTest input injection failed")
		}
	}
	_ = b.flush(b.display)
	return nil
}

func boolToInt32(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

func (b *linuxInputBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.display != 0 && b.close != nil {
		_ = b.close(b.display)
		b.display = 0
	}
	handle, xtstHandle := b.handle, b.xtstHandle
	b.handle, b.xtstHandle = 0, 0
	closeLibrary := b.closeLibraries
	if closeLibrary == nil {
		closeLibrary = func(value uintptr) error { return purego.Dlclose(value) }
	}
	return releaseLinuxInputLibraries(handle, xtstHandle, closeLibrary)
}

func releaseLinuxInputLibraries(handle, xtstHandle uintptr, closeLibrary func(uintptr) error) error {
	var firstErr error
	for _, value := range []uintptr{handle, xtstHandle} {
		if value == 0 {
			continue
		}
		if err := closeLibrary(value); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type linuxUnavailableInputBackend struct{ err error }

func (b *linuxUnavailableInputBackend) SendInputs([]WinInput) error { return b.err }

func linuxInputAction(input WinInput, width, height int) (linuxInputEvent, error) {
	switch input.Type {
	case InputTypeMouse:
		flags := input.Mi.DwFlags
		x, y := int(input.Mi.Dx), int(input.Mi.Dy)
		if flags&MouseEventfAbsolute != 0 {
			if width <= 0 || height <= 0 {
				return linuxInputEvent{}, errors.New("control: X11 screen dimensions are unavailable")
			}
			x = clampLinuxCoordinate(input.Mi.Dx, width)
			y = clampLinuxCoordinate(input.Mi.Dy, height)
		}
		button, down, hasButton := linuxMouseButton(flags, input.Mi.MouseData)
		if flags&MouseEventfWheel != 0 {
			if int32(input.Mi.MouseData) >= 0 {
				return linuxInputEvent{kind: linuxInputClick, button: 4, down: true}, nil
			}
			return linuxInputEvent{kind: linuxInputClick, button: 5, down: true}, nil
		}
		if flags&MouseEventfHWheel != 0 {
			if int32(input.Mi.MouseData) >= 0 {
				return linuxInputEvent{kind: linuxInputClick, button: 6, down: true}, nil
			}
			return linuxInputEvent{kind: linuxInputClick, button: 7, down: true}, nil
		}
		if flags&MouseEventfMove != 0 && hasButton {
			return linuxInputEvent{kind: linuxInputMotionButton, x: x, y: y, button: button, down: down}, nil
		}
		if flags&MouseEventfMove != 0 {
			return linuxInputEvent{kind: linuxInputMotion, x: x, y: y}, nil
		}
		if hasButton {
			return linuxInputEvent{kind: linuxInputButton, button: button, down: down}, nil
		}
		return linuxInputEvent{}, errors.New("control: unsupported X11 mouse event")
	case InputTypeKeyboard:
		if input.Ki.DwFlags&KeyEventfUnicode != 0 {
			return linuxInputEvent{}, errors.New("control: Unicode keyboard input is unavailable through XTest")
		}
		keysym, ok := linuxVKToKeysym(input.Ki.WVk)
		if !ok {
			return linuxInputEvent{}, fmt.Errorf("control: unsupported virtual key %#x on X11", input.Ki.WVk)
		}
		return linuxInputEvent{kind: linuxInputKey, keysym: keysym, down: input.Ki.DwFlags&KeyEventfKeyUp == 0}, nil
	default:
		return linuxInputEvent{}, ErrInvalidInputKind
	}
}

func clampLinuxCoordinate(value int32, extent int) int {
	if value <= 0 {
		return 0
	}
	if value >= 65535 {
		return extent - 1
	}
	return int((int64(value) * int64(extent-1)) / 65535)
}

func linuxMouseButton(flags, data uint32) (uint32, bool, bool) {
	switch {
	case flags&MouseEventfLeftDown != 0:
		return 1, true, true
	case flags&MouseEventfLeftUp != 0:
		return 1, false, true
	case flags&MouseEventfMiddleDown != 0:
		return 2, true, true
	case flags&MouseEventfMiddleUp != 0:
		return 2, false, true
	case flags&MouseEventfRightDown != 0:
		return 3, true, true
	case flags&MouseEventfRightUp != 0:
		return 3, false, true
	case flags&MouseEventfXDown != 0:
		return 7 + data, true, true
	case flags&MouseEventfXUp != 0:
		return 7 + data, false, true
	default:
		return 0, false, false
	}
}

func linuxVKToKeysym(vk uint16) (uint64, bool) {
	switch {
	case vk >= 'A' && vk <= 'Z':
		return uint64(vk + ('a' - 'A')), true
	case vk >= '0' && vk <= '9':
		return uint64(vk), true
	}
	switch vk {
	case 0x08:
		return 0xff08, true
	case 0x09:
		return 0xff09, true
	case 0x0d:
		return 0xff0d, true
	case 0x1b:
		return 0xff1b, true
	case 0x20:
		return 0x20, true
	case 0x10, 0xa0:
		return 0xffe1, true
	case 0xa1:
		return 0xffe2, true
	case 0x11, 0xa2:
		return 0xffe3, true
	case 0xa3:
		return 0xffe4, true
	case 0x12, 0xa4:
		return 0xffe9, true
	case 0xa5:
		return 0xffea, true
	case 0x13:
		return 0xff13, true
	case 0x14:
		return 0xffe5, true
	case 0x15:
		return 0xffe0, true
	case 0x2c: // VK_SNAPSHOT
		return 0xff61, true // XK_Print
	case 0x2d:
		return 0xff63, true
	case 0x2e:
		return 0xffff, true
	case 0x2f: // VK_HELP
		return 0xff6a, true // XK_Help
	case 0x21: // VK_PRIOR
		return 0xff55, true // XK_Prior
	case 0x22:
		return 0xff56, true
	case 0x23:
		return 0xff57, true
	case 0x24:
		return 0xff50, true
	case 0x2b:
		return 0xff8d, true
	case 0x5b:
		return 0xffeb, true
	case 0x5c:
		return 0xffec, true
	case 0x5d:
		return 0xff67, true
	case 0x90:
		return 0xff7f, true
	case 0xba:
		return 0x3b, true
	case 0xbb:
		return 0x3d, true
	case 0xbc:
		return 0x2c, true
	case 0xbd:
		return 0x2d, true
	case 0xbe:
		return 0x2e, true
	case 0xbf:
		return 0x2f, true
	case 0xc0:
		return 0x60, true
	case 0xdb:
		return 0x5b, true
	case 0xdc:
		return 0x5c, true
	case 0xdd:
		return 0x5d, true
	case 0xde:
		return 0x27, true
	case 0xdf:
		return 0x5c, true
	case 0x6a:
		return 0xffaa, true
	case 0x6b:
		return 0xffab, true
	case 0x6d:
		return 0xffad, true
	case 0x6e:
		return 0xffae, true
	case 0x6f:
		return 0xffaf, true
	case 0x60:
		return 0xffb0, true
	case 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67, 0x68, 0x69:
		return uint64(0xffb1 + (vk - 0x61)), true
	case 0x6c: // VK_SEPARATOR
		return 0xffac, true // XK_KP_Separator (was KP_5 by mistake)
	case 0x25:
		return 0xff51, true
	case 0x26:
		return 0xff52, true
	case 0x27:
		return 0xff53, true
	case 0x28:
		return 0xff54, true
	case 0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78, 0x79, 0x7a, 0x7b:
		return uint64(0xffbe + (vk - 0x70)), true
	}
	return linuxExtraVKKeysym(vk)
}

// linuxExtraVKKeysyms extends the classic map with F13-F24, lock, IME and
// XF86 media/browser keys. Keys without a keycode in the active X keymap are
// still rejected by SendInputs with a clear error.
var linuxExtraVKKeysyms = map[uint16]uint64{
	0x0c: 0xff0b,     // VK_CLEAR -> XK_Clear
	0x19: 0xff34,     // VK_HANJA -> XK_Hangul_Hanja
	0x1c: 0xff23,     // VK_CONVERT -> XK_Henkan
	0x1d: 0xff22,     // VK_NONCONVERT -> XK_Muhenkan
	0x29: 0xff60,     // VK_SELECT -> XK_Select
	0x2a: 0xff61,     // VK_PRINT -> XK_Print
	0x5f: 0x1008ff2f, // VK_SLEEP -> XF86Sleep
	0x91: 0xff14,     // VK_SCROLL -> XK_Scroll_Lock
	0xa6: 0x1008ff26, // VK_BROWSER_BACK -> XF86Back
	0xa7: 0x1008ff27, // VK_BROWSER_FORWARD -> XF86Forward
	0xa8: 0x1008ff29, // VK_BROWSER_REFRESH -> XF86Refresh
	0xa9: 0x1008ff28, // VK_BROWSER_STOP -> XF86Stop
	0xaa: 0x1008ff1b, // VK_BROWSER_SEARCH -> XF86Search
	0xab: 0x1008ff30, // VK_BROWSER_FAVORITES -> XF86Favorites
	0xac: 0x1008ff18, // VK_BROWSER_HOME -> XF86HomePage
	0xad: 0x1008ff12, // VK_VOLUME_MUTE -> XF86AudioMute
	0xae: 0x1008ff11, // VK_VOLUME_DOWN -> XF86AudioLowerVolume
	0xaf: 0x1008ff13, // VK_VOLUME_UP -> XF86AudioRaiseVolume
	0xb0: 0x1008ff17, // VK_MEDIA_NEXT_TRACK -> XF86AudioNext
	0xb1: 0x1008ff16, // VK_MEDIA_PREV_TRACK -> XF86AudioPrev
	0xb2: 0x1008ff15, // VK_MEDIA_STOP -> XF86AudioStop
	0xb3: 0x1008ff14, // VK_MEDIA_PLAY_PAUSE -> XF86AudioPlay
	0xb4: 0x1008ff19, // VK_LAUNCH_MAIL -> XF86Mail
	0xe2: 0x3c,       // VK_OEM_102 -> XK_less (ISO key left of Z)
}

func linuxExtraVKKeysym(vk uint16) (uint64, bool) {
	if vk >= 0x7c && vk <= 0x87 { // VK_F13..VK_F24 -> XK_F13..XK_F24
		return uint64(0xffca + (vk - 0x7c)), true
	}
	keysym, ok := linuxExtraVKKeysyms[vk]
	return keysym, ok
}
