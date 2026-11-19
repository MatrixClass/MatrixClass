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
	mu      sync.Mutex
	handle  uintptr
	display uintptr
	screen  int32
	width   int32
	height  int32
	close   func(uintptr) int32
	flush   func(uintptr) int32
	motion  func(uintptr, int32, int32, int32, uint64) int32
	button  func(uintptr, uint32, int32, uint64) int32
	key     func(uintptr, uint32, int32, uint64) int32
	keycode func(uintptr, uint64) byte
}

func newPlatformInputBackend() InputBackend {
	backend, err := newLinuxInputBackend()
	if err != nil {
		return &linuxUnavailableInputBackend{err: err}
	}
	return backend
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
		_ = purego.Dlclose(x11)
		return nil, fmt.Errorf("control: load libXtst: %w", err)
	}
	lookupX11 := func(name string) uintptr { addr, _ := purego.Dlsym(x11, name); return addr }
	lookupXtst := func(name string) uintptr { addr, _ := purego.Dlsym(xtst, name); return addr }
	if lookupX11("XOpenDisplay") == 0 || lookupX11("XCloseDisplay") == 0 || lookupX11("XDefaultScreen") == 0 || lookupX11("XDisplayWidth") == 0 || lookupX11("XDisplayHeight") == 0 || lookupX11("XFlush") == 0 || lookupX11("XKeysymToKeycode") == 0 || lookupXtst("XTestFakeMotionEvent") == 0 || lookupXtst("XTestFakeButtonEvent") == 0 || lookupXtst("XTestFakeKeyEvent") == 0 {
		_ = purego.Dlclose(xtst)
		_ = purego.Dlclose(x11)
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
		_ = purego.Dlclose(xtst)
		_ = purego.Dlclose(x11)
		return nil, ErrInputUnsupportedPlatform
	}
	screen := defaultScreen(display)
	return &linuxInputBackend{handle: x11, display: display, screen: screen, width: displayWidth(display, screen), height: displayHeight(display, screen), close: closeDisplay, flush: flush, motion: fakeMotion, button: fakeButton, key: fakeKey, keycode: keysymToKeycode}, nil
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
	if b.handle != 0 {
		err := purego.Dlclose(b.handle)
		b.handle = 0
		return err
	}
	return nil
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
			x = int((int64(input.Mi.Dx) * int64(width-1)) / 65535)
			y = int((int64(input.Mi.Dy) * int64(height-1)) / 65535)
		}
		button, down, hasButton := linuxMouseButton(flags, input.Mi.MouseData)
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
		keysym, ok := linuxVKToKeysym(input.Ki.WVk)
		if !ok {
			return linuxInputEvent{}, fmt.Errorf("control: unsupported virtual key %#x on X11", input.Ki.WVk)
		}
		return linuxInputEvent{kind: linuxInputKey, keysym: keysym, down: input.Ki.DwFlags&KeyEventfKeyUp == 0}, nil
	default:
		return linuxInputEvent{}, ErrInvalidInputKind
	}
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
		return 4 + data, true, true
	case flags&MouseEventfXUp != 0:
		return 4 + data, false, true
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
	default:
		return 0, false
	}
}
