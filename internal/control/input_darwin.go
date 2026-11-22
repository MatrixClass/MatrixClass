//go:build darwin

package control

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

var (
	ErrDarwinAccessibilityRequired = errors.New("control: macOS Accessibility permission is required for CGEvent input")
	ErrDarwinInputUnavailable      = errors.New("control: macOS CGEvent input backend is unavailable in this build")
	ErrDarwinUnsupportedKey        = errors.New("control: unsupported macOS virtual key")
)

const (
	darwinRTLDNow                                             = 2
	darwinMouseMove, darwinMouseButtonKind, darwinMouseScroll = "move", "button", "scroll"

	// CGMouseButton.
	darwinButtonLeft, darwinButtonRight, darwinButtonMiddle = 0, 1, 2
	darwinButtonBack, darwinButtonForward                   = 3, 4

	// CGEventType.
	darwinEventLeftDown, darwinEventLeftUp, darwinEventRightDown, darwinEventRightUp = 1, 2, 3, 4
	darwinEventMouseMoved, darwinEventLeftDragged, darwinEventRightDragged           = 5, 6, 7
	darwinEventOtherDown, darwinEventOtherUp, darwinEventOtherDragged                = 25, 26, 27
	darwinEventScroll                                                                = 22

	darwinScrollUnitPixel      = 0
	darwinHIDEventTap          = 0
	darwinMouseEventClickState = 1

	// Windows wheel delta per notch and the pixels scrolled per notch.
	darwinWheelDelta      = 120
	darwinPixelsPerNotch  = 40
	darwinDoubleClickTime = 500 * time.Millisecond
	darwinDoubleClickDist = 4.0
)

type darwinMouseEvent struct {
	kind      string
	x, y      float64
	button    uint32
	down      bool
	delta     int32
	hasPos    bool
	eventType uint32
	clicks    int64
}

type darwinKeyboardEvent struct {
	keyCode uint16
	down    bool
}

type darwinCGPoint struct {
	x float64
	y float64
}

// darwinInputState is the pure (native-free) part of input translation:
// pressed buttons (for drag events), modifier flags, click counting and
// scroll remainders. It is guarded by darwinInputBackend.mu.
type darwinInputState struct {
	buttons     uint32 // bit per CGMouseButton
	modifiers   map[uint16]uint64
	x, y        float64
	hasPos      bool
	lastClick   time.Time
	lastButton  uint32
	lastClickX  float64
	lastClickY  float64
	clickCount  int64
	scrollCarry int32
}

type darwinInputBackend struct {
	mu                                sync.Mutex
	applicationServices, coreGraphics uintptr
	trusted                           func() bool
	createMouse                       func(uintptr, uint32, darwinCGPoint, uint32) uintptr
	createKeyboard                    func(uintptr, uint16, bool) uintptr
	createScroll                      func(source uintptr, units, wheelCount uint32, wheel1, wheel2, wheel3 int32) uintptr
	createEvent                       func(uintptr) uintptr
	getLocation                       func(uintptr) darwinCGPoint
	setFlags                          func(uintptr, uint64)
	setIntegerField                   func(uintptr, uint32, int64)
	post                              func(uint32, uintptr)
	release                           func(uintptr)
	mainDisplay                       func() uint32
	displayWidth                      func(uint32) uint64
	displayHeight                     func(uint32) uint64
	state                             darwinInputState
	now                               func() time.Time
}

type darwinUnavailableInputBackend struct{ err error }

func newPlatformInputBackend() InputBackend {
	backend, err := newDarwinInputBackend()
	if err != nil {
		return &darwinUnavailableInputBackend{err: err}
	}
	return backend
}

// newDarwinInputBackend binds the CoreGraphics entry points. It does not
// require Accessibility up front: trust is checked on every batch so a
// permission granted after startup takes effect without restarting.
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
	b := &darwinInputBackend{applicationServices: services, coreGraphics: graphics, now: time.Now}
	bind := []struct {
		handle uintptr
		name   string
		fn     any
	}{
		{services, "AXIsProcessTrusted", &b.trusted},
		{graphics, "CGEventCreateMouseEvent", &b.createMouse},
		{graphics, "CGEventCreateKeyboardEvent", &b.createKeyboard},
		// The non-variadic variant: calling the variadic
		// CGEventCreateScrollWheelEvent through purego would pass wheel1 in a
		// register, but Apple arm64 reads variadic arguments from the stack.
		{graphics, "CGEventCreateScrollWheelEvent2", &b.createScroll},
		{graphics, "CGEventCreate", &b.createEvent},
		{graphics, "CGEventGetLocation", &b.getLocation},
		{graphics, "CGEventSetFlags", &b.setFlags},
		{graphics, "CGEventSetIntegerValueField", &b.setIntegerField},
		{graphics, "CGEventPost", &b.post},
		{graphics, "CFRelease", &b.release},
		{graphics, "CGMainDisplayID", &b.mainDisplay},
		{graphics, "CGDisplayPixelsWide", &b.displayWidth},
		{graphics, "CGDisplayPixelsHigh", &b.displayHeight},
	}
	for _, entry := range bind {
		address, err := purego.Dlsym(entry.handle, entry.name)
		if err != nil || address == 0 {
			b.close()
			return nil, errors.Join(ErrDarwinInputUnavailable, fmt.Errorf("missing symbol %s", entry.name))
		}
		purego.RegisterFunc(entry.fn, address)
	}
	return b, nil
}

func (b *darwinUnavailableInputBackend) SendInputs([]WinInput) error { return b.err }

// displaySize returns the main display size in points, which is the
// coordinate space of CGEvent locations (correct on Retina displays). The
// main display's origin is (0,0) in global coordinates, so absolute input is
// mapped onto the main display, matching the captured screen.
func (b *darwinInputBackend) displaySize() (int, int) {
	display := b.mainDisplay()
	return int(b.displayWidth(display)), int(b.displayHeight(display))
}

func (b *darwinInputBackend) cursorLocation() (darwinCGPoint, bool) {
	event := b.createEvent(0)
	if event == 0 {
		return darwinCGPoint{}, false
	}
	defer b.release(event)
	return b.getLocation(event), true
}

func (b *darwinInputBackend) SendInputs(inputs []WinInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.trusted() {
		return ErrDarwinAccessibilityRequired
	}
	if len(inputs) == 0 {
		return nil
	}
	width, height := b.displaySize()
	now := time.Now
	if b.now != nil {
		now = b.now
	}
	for _, input := range inputs {
		var event uintptr
		switch input.Type {
		case InputTypeMouse:
			if !b.state.hasPos && input.Mi.DwFlags&MouseEventfAbsolute == 0 {
				if point, ok := b.cursorLocation(); ok {
					b.state.x, b.state.y, b.state.hasPos = point.x, point.y, true
				}
			}
			action, err := b.state.mouse(input, width, height, now())
			if err != nil {
				return err
			}
			switch action.kind {
			case darwinMouseScroll:
				if action.delta == 0 {
					continue
				}
				event = b.createScroll(0, darwinScrollUnitPixel, 1, action.delta, 0, 0)
			default:
				event = b.createMouse(0, action.eventType, darwinCGPoint{x: action.x, y: action.y}, action.button)
				if event != 0 && action.clicks > 0 {
					b.setIntegerField(event, darwinMouseEventClickState, action.clicks)
				}
			}
		case InputTypeKeyboard:
			action, err := darwinKeyboardAction(input)
			if err != nil {
				return err
			}
			b.state.updateModifier(action.keyCode, action.down)
			event = b.createKeyboard(0, action.keyCode, action.down)
		default:
			return ErrInvalidInputKind
		}
		if event == 0 {
			return ErrDarwinInputUnavailable
		}
		// Synthetic events do not inherit modifier state from earlier
		// synthetic modifier key presses, so apply it explicitly (this makes
		// Shift+A, Cmd+C and Cmd+click work).
		b.setFlags(event, b.state.flags())
		b.post(darwinHIDEventTap, event)
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

// darwinScaleAbsolute maps a 0..65535 normalized coordinate onto [0, size).
func darwinScaleAbsolute(value int32, size int) float64 {
	if value < 0 {
		value = 0
	}
	if value > 65535 {
		value = 65535
	}
	return float64(value) * float64(size) / 65536
}

// darwinMouseAction is the stateless translation kept for compatibility with
// existing callers/tests; it has no drag or click-count context.
func darwinMouseAction(input WinInput, width, height int) (darwinMouseEvent, error) {
	var state darwinInputState
	return state.mouse(input, width, height, time.Time{})
}

func (s *darwinInputState) mouse(input WinInput, width, height int, now time.Time) (darwinMouseEvent, error) {
	if width <= 0 || height <= 0 {
		return darwinMouseEvent{}, ErrDarwinInputUnavailable
	}
	flags := input.Mi.DwFlags
	if flags&MouseEventfWheel != 0 {
		return darwinMouseEvent{kind: darwinMouseScroll, delta: s.scrollPixels(int32(input.Mi.MouseData))}, nil
	}
	switch {
	case flags&MouseEventfAbsolute != 0:
		s.x = darwinScaleAbsolute(input.Mi.Dx, width)
		s.y = darwinScaleAbsolute(input.Mi.Dy, height)
		s.hasPos = true
	case flags&MouseEventfMove != 0:
		s.x = math.Max(0, math.Min(float64(width-1), s.x+float64(input.Mi.Dx)))
		s.y = math.Max(0, math.Min(float64(height-1), s.y+float64(input.Mi.Dy)))
		s.hasPos = true
	}
	if button, down, ok := darwinMouseButton(flags, input.Mi.MouseData); ok {
		event := darwinMouseEvent{kind: darwinMouseButtonKind, x: s.x, y: s.y, button: button, down: down, hasPos: s.hasPos, eventType: darwinMouseEventType(button, down)}
		event.clicks = s.click(button, down, now)
		if down {
			s.buttons |= 1 << button
		} else {
			s.buttons &^= 1 << button
		}
		return event, nil
	}
	if flags&MouseEventfMove != 0 {
		eventType, button := s.moveEventType()
		return darwinMouseEvent{kind: darwinMouseMove, x: s.x, y: s.y, hasPos: true, eventType: eventType, button: button}, nil
	}
	return darwinMouseEvent{}, errors.New("control: unsupported macOS mouse event")
}

// moveEventType returns a drag event while a button is held; macOS apps
// ignore plain mouse-moved events for drags and selections.
func (s *darwinInputState) moveEventType() (uint32, uint32) {
	switch {
	case s.buttons&(1<<darwinButtonLeft) != 0:
		return darwinEventLeftDragged, darwinButtonLeft
	case s.buttons&(1<<darwinButtonRight) != 0:
		return darwinEventRightDragged, darwinButtonRight
	case s.buttons != 0:
		for button := uint32(darwinButtonMiddle); button < 32; button++ {
			if s.buttons&(1<<button) != 0 {
				return darwinEventOtherDragged, button
			}
		}
	}
	return darwinEventMouseMoved, darwinButtonLeft
}

// click returns the kCGMouseEventClickState value; double/triple clicks are
// only recognised by AppKit when this is set.
func (s *darwinInputState) click(button uint32, down bool, now time.Time) int64 {
	if !down {
		if s.clickCount == 0 {
			return 1
		}
		return s.clickCount
	}
	if s.clickCount > 0 && button == s.lastButton && !now.IsZero() && now.Sub(s.lastClick) <= darwinDoubleClickTime &&
		math.Abs(s.x-s.lastClickX) <= darwinDoubleClickDist && math.Abs(s.y-s.lastClickY) <= darwinDoubleClickDist {
		s.clickCount++
	} else {
		s.clickCount = 1
	}
	s.lastButton, s.lastClick, s.lastClickX, s.lastClickY = button, now, s.x, s.y
	return s.clickCount
}

// scrollPixels converts Windows wheel units to pixels, carrying fractions so
// high-resolution (trackpad) deltas are not lost.
func (s *darwinInputState) scrollPixels(delta int32) int32 {
	total := int64(s.scrollCarry) + int64(delta)*darwinPixelsPerNotch
	pixels := total / darwinWheelDelta
	s.scrollCarry = int32(total - pixels*darwinWheelDelta)
	if pixels > math.MaxInt32 {
		pixels = math.MaxInt32
	} else if pixels < math.MinInt32 {
		pixels = math.MinInt32
	}
	return int32(pixels)
}

func (s *darwinInputState) updateModifier(keyCode uint16, down bool) {
	flag, ok := darwinModifierFlag(keyCode)
	if !ok {
		return
	}
	if s.modifiers == nil {
		s.modifiers = make(map[uint16]uint64)
	}
	if down {
		s.modifiers[keyCode] = flag
	} else {
		delete(s.modifiers, keyCode)
	}
}

func (s *darwinInputState) flags() uint64 {
	var flags uint64
	for _, flag := range s.modifiers {
		flags |= flag
	}
	return flags
}

func darwinModifierFlag(keyCode uint16) (uint64, bool) {
	switch keyCode {
	case 56, 60: // shift, right shift
		return darwinFlagShift, true
	case 59, 62: // control, right control
		return darwinFlagControl, true
	case 58, 61: // option, right option
		return darwinFlagOption, true
	case 55, 54: // command, right command
		return darwinFlagCommand, true
	}
	return 0, false
}

func darwinMouseButton(flags, mouseData uint32) (uint32, bool, bool) {
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
	case flags&(MouseEventfXDown|MouseEventfXUp) != 0:
		button := uint32(darwinButtonBack)
		if mouseData&XButton2 != 0 {
			button = darwinButtonForward
		}
		return button, flags&MouseEventfXDown != 0, true
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

// darwinVKKeyCodes maps Windows virtual-key codes (as produced by the
// teacher UI) to macOS kVK_* virtual key codes (ANSI layout positions).
var darwinVKKeyCodes = map[uint16]uint16{
	// Letters.
	'A': 0, 'S': 1, 'D': 2, 'F': 3, 'H': 4, 'G': 5, 'Z': 6, 'X': 7, 'C': 8, 'V': 9,
	'B': 11, 'Q': 12, 'W': 13, 'E': 14, 'R': 15, 'Y': 16, 'T': 17, 'O': 31, 'U': 32,
	'I': 34, 'P': 35, 'L': 37, 'J': 38, 'K': 40, 'N': 45, 'M': 46,
	// Digits.
	'1': 18, '2': 19, '3': 20, '4': 21, '6': 22, '5': 23, '9': 25, '7': 26, '8': 28, '0': 29,
	// OEM punctuation (VK_OEM_*). Windows never uses ASCII punctuation as VKs:
	// 0x2C-0x2E are PrintScreen/Insert/Delete and 0x5B-0x5D the Windows/Apps keys.
	0xBA: 41, // ; :
	0xBB: 24, // = +
	0xBC: 43, // , <
	0xBD: 27, // - _
	0xBE: 47, // . >
	0xBF: 44, // / ?
	0xC0: 50, // ` ~
	0xDB: 33, // [ {
	0xDC: 42, // \ |
	0xDD: 30, // ] }
	0xDE: 39, // ' "
	0xE2: 10, // ISO section key
	// Editing and navigation.
	0x08: 51,  // Backspace -> Delete
	0x09: 48,  // Tab
	0x0D: 36,  // Enter -> Return
	0x1B: 53,  // Escape
	0x20: 49,  // Space
	0x14: 57,  // Caps Lock
	0x21: 116, // Page Up
	0x22: 121, // Page Down
	0x23: 119, // End
	0x24: 115, // Home
	0x25: 123, // Left
	0x26: 126, // Up
	0x27: 124, // Right
	0x28: 125, // Down
	0x2D: 114, // Insert -> Help
	0x2E: 117, // Delete -> Forward Delete
	// Modifiers. Windows key = Command, Alt = Option.
	0x10: 56, 0xA0: 56, 0xA1: 60, // Shift, LShift, RShift
	0x11: 59, 0xA2: 59, 0xA3: 62, // Control, LControl, RControl
	0x12: 58, 0xA4: 58, 0xA5: 61, // Alt, LAlt, RAlt
	0x5B: 55, 0x5C: 54, // LWin, RWin
	// Function keys F1-F20.
	0x70: 122, 0x71: 120, 0x72: 99, 0x73: 118, 0x74: 96, 0x75: 97, 0x76: 98, 0x77: 100,
	0x78: 101, 0x79: 109, 0x7A: 103, 0x7B: 111, 0x7C: 105, 0x7D: 107, 0x7E: 113, 0x7F: 106,
	0x80: 64, 0x81: 79, 0x82: 80, 0x83: 90,
	// Numeric keypad.
	0x60: 82, 0x61: 83, 0x62: 84, 0x63: 85, 0x64: 86, 0x65: 87, 0x66: 88, 0x67: 89, 0x68: 91, 0x69: 92,
	0x6A: 67, // Multiply
	0x6B: 69, // Add
	0x6D: 78, // Subtract
	0x6E: 65, // Decimal
	0x6F: 75, // Divide
	0x90: 71, // Num Lock -> Keypad Clear
	// Media keys that have a direct kVK equivalent.
	0xAD: 74, // Volume Mute
	0xAE: 73, // Volume Down
	0xAF: 72, // Volume Up
}

func darwinVKToKeyCode(vk uint16) (uint16, bool) {
	keyCode, ok := darwinVKKeyCodes[vk]
	return keyCode, ok
}
