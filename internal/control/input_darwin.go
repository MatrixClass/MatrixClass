//go:build darwin

package control

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

var (
	ErrDarwinAccessibilityRequired = errors.New("control: macOS Accessibility permission is required for CGEvent input")
	ErrDarwinInputUnavailable      = errors.New("control: macOS CGEvent input backend is unavailable in this build")
	ErrDarwinUnsupportedKey        = errors.New("control: unsupported macOS virtual key")
)

const (
	darwinRTLDNow                                                                      = 2
	darwinMouseMove, darwinMouseButtonKind, darwinMouseScroll                          = "move", "button", "scroll"
	darwinButtonLeft, darwinButtonRight, darwinButtonMiddle                            = 0, 1, 2
	darwinEventLeftDown, darwinEventLeftUp, darwinEventRightDown, darwinEventRightUp   = 1, 2, 3, 4
	darwinEventMouseMoved, darwinEventOtherDown, darwinEventOtherUp, darwinEventScroll = 5, 25, 26, 22
)

type darwinMouseEvent struct {
	kind   string
	x, y   float64
	button uint32
	down   bool
	delta  int32
}
type darwinKeyboardEvent struct {
	keyCode uint16
	down    bool
}

type darwinCGPoint struct {
	x float64
	y float64
}

type darwinInputBackend struct {
	mu                                sync.Mutex
	applicationServices, coreGraphics uintptr
	trusted                           func() bool
	createMouse                       func(uintptr, uint32, darwinCGPoint, uint32) uintptr
	createKeyboard                    func(uintptr, uint16, bool) uintptr
	createScroll                      func(uintptr, uint32, uint32, int32) uintptr
	post                              func(uint32, uintptr)
	release                           func(uintptr)
	mainDisplay                       func() uint32
	displayWidth                      func(uint32) uint64
	displayHeight                     func(uint32) uint64
	width, height                     int
}

type darwinUnavailableInputBackend struct{ err error }

func newPlatformInputBackend() InputBackend {
	backend, err := newDarwinInputBackend()
	if err != nil {
		return &darwinUnavailableInputBackend{err: err}
	}
	return backend
}

func newDarwinInputBackend() (*darwinInputBackend, error) {
	services, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", darwinRTLDNow)
	if err != nil {
		return nil, errors.Join(ErrDarwinInputUnavailable, err)
	}
	graphics, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", darwinRTLDNow)
	if err != nil {
		_ = purego.Dlclose(services)
		return nil, errors.Join(ErrDarwinInputUnavailable, err)
	}
	lookup := func(handle uintptr, name string) uintptr { address, _ := purego.Dlsym(handle, name); return address }
	addresses := []uintptr{
		lookup(services, "AXIsProcessTrusted"), lookup(graphics, "CGEventCreateMouseEvent"),
		lookup(graphics, "CGEventCreateKeyboardEvent"), lookup(graphics, "CGEventCreateScrollWheelEvent"),
		lookup(graphics, "CGEventPost"), lookup(graphics, "CFRelease"), lookup(graphics, "CGMainDisplayID"),
		lookup(graphics, "CGDisplayPixelsWide"), lookup(graphics, "CGDisplayPixelsHigh"),
	}
	for _, address := range addresses {
		if address == 0 {
			_ = purego.Dlclose(graphics)
			_ = purego.Dlclose(services)
			return nil, ErrDarwinInputUnavailable
		}
	}
	b := &darwinInputBackend{applicationServices: services, coreGraphics: graphics}
	purego.RegisterFunc(&b.trusted, addresses[0])
	purego.RegisterFunc(&b.createMouse, addresses[1])
	purego.RegisterFunc(&b.createKeyboard, addresses[2])
	purego.RegisterFunc(&b.createScroll, addresses[3])
	purego.RegisterFunc(&b.post, addresses[4])
	purego.RegisterFunc(&b.release, addresses[5])
	purego.RegisterFunc(&b.mainDisplay, addresses[6])
	purego.RegisterFunc(&b.displayWidth, addresses[7])
	purego.RegisterFunc(&b.displayHeight, addresses[8])
	if !b.trusted() {
		b.close()
		return nil, ErrDarwinAccessibilityRequired
	}
	display := b.mainDisplay()
	b.width, b.height = int(b.displayWidth(display)), int(b.displayHeight(display))
	if b.width <= 0 || b.height <= 0 {
		b.close()
		return nil, errors.Join(ErrDarwinInputUnavailable, errors.New("macOS main display dimensions are unavailable"))
	}
	return b, nil
}

func (b *darwinUnavailableInputBackend) SendInputs([]WinInput) error { return b.err }

func (b *darwinInputBackend) SendInputs(inputs []WinInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.trusted() {
		return ErrDarwinAccessibilityRequired
	}
	for _, input := range inputs {
		var event uintptr
		switch input.Type {
		case InputTypeMouse:
			action, err := darwinMouseAction(input, b.width, b.height)
			if err != nil {
				return err
			}
			switch action.kind {
			case darwinMouseMove:
				event = b.createMouse(0, darwinEventMouseMoved, darwinCGPoint{x: action.x, y: action.y}, darwinButtonLeft)
			case darwinMouseButtonKind:
				event = b.createMouse(0, darwinMouseEventType(action.button, action.down), darwinCGPoint{x: action.x, y: action.y}, action.button)
			case darwinMouseScroll:
				event = b.createScroll(0, 1, 1, action.delta)
			}
		case InputTypeKeyboard:
			action, err := darwinKeyboardAction(input)
			if err != nil {
				return err
			}
			event = b.createKeyboard(0, action.keyCode, action.down)
		default:
			return ErrInvalidInputKind
		}
		if event == 0 {
			return ErrDarwinInputUnavailable
		}
		b.post(0, event)
		b.release(event)
	}
	return nil
}

func (b *darwinInputBackend) close() {
	if b.coreGraphics != 0 {
		_ = purego.Dlclose(b.coreGraphics)
		b.coreGraphics = 0
	}
	if b.applicationServices != 0 {
		_ = purego.Dlclose(b.applicationServices)
		b.applicationServices = 0
	}
}

func darwinMouseAction(input WinInput, width, height int) (darwinMouseEvent, error) {
	if width <= 0 || height <= 0 {
		return darwinMouseEvent{}, ErrDarwinInputUnavailable
	}
	flags := input.Mi.DwFlags
	if flags&MouseEventfWheel != 0 {
		return darwinMouseEvent{kind: darwinMouseScroll, delta: int32(input.Mi.MouseData)}, nil
	}
	x, y := float64(input.Mi.Dx), float64(input.Mi.Dy)
	if flags&MouseEventfAbsolute != 0 {
		x = float64(input.Mi.Dx) * float64(width-1) / 65535
		y = float64(input.Mi.Dy) * float64(height-1) / 65535
	}
	if button, down, ok := darwinMouseButton(flags); ok {
		return darwinMouseEvent{kind: darwinMouseButtonKind, x: x, y: y, button: button, down: down}, nil
	}
	if flags&MouseEventfMove != 0 {
		return darwinMouseEvent{kind: darwinMouseMove, x: x, y: y}, nil
	}
	return darwinMouseEvent{}, errors.New("control: unsupported macOS mouse event")
}

func darwinMouseButton(flags uint32) (uint32, bool, bool) {
	switch {
	case flags&MouseEventfLeftDown != 0:
		return darwinButtonLeft, true, true
	case flags&MouseEventfLeftUp != 0:
		return darwinButtonLeft, false, true
	case flags&MouseEventfRightDown != 0:
		return darwinButtonRight, true, true
	case flags&MouseEventfRightUp != 0:
		return darwinButtonRight, false, true
	case flags&MouseEventfMiddleDown != 0:
		return darwinButtonMiddle, true, true
	case flags&MouseEventfMiddleUp != 0:
		return darwinButtonMiddle, false, true
	default:
		return 0, false, false
	}
}

func darwinMouseEventType(button uint32, down bool) uint32 {
	switch button {
	case darwinButtonLeft:
		if down {
			return darwinEventLeftDown
		}
		return darwinEventLeftUp
	case darwinButtonRight:
		if down {
			return darwinEventRightDown
		}
		return darwinEventRightUp
	default:
		if down {
			return darwinEventOtherDown
		}
		return darwinEventOtherUp
	}
}

func darwinKeyboardAction(input WinInput) (darwinKeyboardEvent, error) {
	keyCode, ok := darwinVKToKeyCode(input.Ki.WVk)
	if !ok {
		return darwinKeyboardEvent{}, fmt.Errorf("%w: %#x", ErrDarwinUnsupportedKey, input.Ki.WVk)
	}
	return darwinKeyboardEvent{keyCode: keyCode, down: input.Ki.DwFlags&KeyEventfKeyUp == 0}, nil
}

func darwinVKToKeyCode(vk uint16) (uint16, bool) {
	keys := map[uint16]uint16{'A': 0, 'S': 1, 'D': 2, 'F': 3, 'H': 4, 'G': 5, 'Z': 6, 'X': 7, 'C': 8, 'V': 9, 'B': 11, 'Q': 12, 'W': 13, 'E': 14, 'R': 15, 'Y': 16, 'T': 17, '1': 18, '2': 19, '3': 20, '4': 21, '6': 22, '5': 23, '=': 24, '9': 25, '7': 26, '-': 27, '8': 28, '0': 29, ']': 30, 'O': 31, 'U': 32, '[': 33, 'I': 34, 'P': 35, 'L': 37, 'J': 38, 'K': 40, ';': 41, '\\': 42, ',': 43, '/': 44, 'N': 45, 'M': 46, '.': 47, '`': 50}
	if keyCode, ok := keys[vk]; ok {
		return keyCode, true
	}
	special := map[uint16]uint16{0x08: 51, 0x09: 48, 0x0D: 36, 0x1B: 53, 0x20: 49, 0x25: 123, 0x26: 126, 0x27: 124, 0x28: 125, 0x70: 122, 0x71: 120, 0x72: 99, 0x73: 118, 0x74: 96, 0x75: 97, 0x76: 98, 0x77: 100, 0x78: 101, 0x79: 109, 0x7A: 103, 0x7B: 111}
	keyCode, ok := special[vk]
	return keyCode, ok
}
