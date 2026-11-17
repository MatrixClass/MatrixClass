package control

import (
	"errors"
	"fmt"
	"math"
	"nexlab/internal/core"
)

const (
	InputTypeMouse    uint32 = 0
	InputTypeKeyboard uint32 = 1

	MouseEventfMove        uint32 = 0x0001
	MouseEventfLeftDown    uint32 = 0x0002
	MouseEventfLeftUp      uint32 = 0x0004
	MouseEventfRightDown   uint32 = 0x0008
	MouseEventfRightUp     uint32 = 0x0010
	MouseEventfMiddleDown  uint32 = 0x0020
	MouseEventfMiddleUp    uint32 = 0x0040
	MouseEventfXDown       uint32 = 0x0080
	MouseEventfXUp         uint32 = 0x0100
	MouseEventfWheel       uint32 = 0x0800
	MouseEventfHWheel      uint32 = 0x1000
	MouseEventfVirtualDesk uint32 = 0x4000
	MouseEventfAbsolute    uint32 = 0x8000

	KeyEventfExtendedKey uint32 = 0x0001
	KeyEventfKeyUp       uint32 = 0x0002
	KeyEventfUnicode     uint32 = 0x0004
	KeyEventfScancode    uint32 = 0x0008

	XButton1 uint32 = 0x0001
	XButton2 uint32 = 0x0002
)

var (
	ErrInvalidInputKind  = errors.New("control: unsupported input event kind")
	ErrUnsupportedButton = errors.New("control: unsupported mouse button")
	ErrInvalidVKCode     = errors.New("control: virtual key code must be non-zero")
)

type MouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type KeybdInput struct {
	WVk         uint16
	WScan       uint16
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type WinInput struct {
	Type uint32
	Mi   MouseInput
	Ki   KeybdInput
}

type InputBackend interface {
	SendInputs(inputs []WinInput) error
}

type InputInjector struct {
	backend InputBackend
}

func NewInputInjector(backend InputBackend) *InputInjector {
	if backend == nil {
		backend = newPlatformInputBackend()
	}
	return &InputInjector{backend: backend}
}

func (inj *InputInjector) Inject(ev core.RemoteInputEvent) error {
	inputs, err := BuildInputs(ev)
	if err != nil {
		return err
	}
	if len(inputs) == 0 {
		return nil
	}
	return inj.backend.SendInputs(inputs)
}

func NormalizeCoord(norm float64) int32 {
	if math.IsNaN(norm) || norm <= 0 {
		return 0
	}
	if norm >= 1 {
		return 65535
	}
	return int32(math.Round(norm * 65535.0))
}

func BuildInputs(ev core.RemoteInputEvent) ([]WinInput, error) {
	switch ev.Kind {
	case core.InputMouseMove:
		return []WinInput{{
			Type: InputTypeMouse,
			Mi: MouseInput{
				Dx:      NormalizeCoord(ev.X),
				Dy:      NormalizeCoord(ev.Y),
				DwFlags: MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk,
			},
		}}, nil

	case core.InputMouseDown:
		flag, data, err := mouseButtonFlag(ev.Button, true)
		if err != nil {
			return nil, err
		}
		mi := MouseInput{
			MouseData: data,
			DwFlags:   flag,
		}
		if ev.X >= 0 && ev.Y >= 0 {
			mi.Dx = NormalizeCoord(ev.X)
			mi.Dy = NormalizeCoord(ev.Y)
			mi.DwFlags |= MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk
		}
		return []WinInput{{Type: InputTypeMouse, Mi: mi}}, nil

	case core.InputMouseUp:
		flag, data, err := mouseButtonFlag(ev.Button, false)
		if err != nil {
			return nil, err
		}
		mi := MouseInput{
			MouseData: data,
			DwFlags:   flag,
		}
		if ev.X >= 0 && ev.Y >= 0 {
			mi.Dx = NormalizeCoord(ev.X)
			mi.Dy = NormalizeCoord(ev.Y)
			mi.DwFlags |= MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk
		}
		return []WinInput{{Type: InputTypeMouse, Mi: mi}}, nil

	case core.InputMouseWheel:
		return []WinInput{{
			Type: InputTypeMouse,
			Mi: MouseInput{
				MouseData: uint32(int32(ev.Delta)),
				DwFlags:   MouseEventfWheel,
			},
		}}, nil

	case core.InputKeyDown:
		if ev.VKCode == 0 {
			return nil, ErrInvalidVKCode
		}
		var flags uint32
		if isExtendedVK(ev.VKCode) {
			flags |= KeyEventfExtendedKey
		}
		return []WinInput{{
			Type: InputTypeKeyboard,
			Ki: KeybdInput{
				WVk:     ev.VKCode,
				DwFlags: flags,
			},
		}}, nil

	case core.InputKeyUp:
		if ev.VKCode == 0 {
			return nil, ErrInvalidVKCode
		}
		flags := KeyEventfKeyUp
		if isExtendedVK(ev.VKCode) {
			flags |= KeyEventfExtendedKey
		}
		return []WinInput{{
			Type: InputTypeKeyboard,
			Ki: KeybdInput{
				WVk:     ev.VKCode,
				DwFlags: flags,
			},
		}}, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrInvalidInputKind, ev.Kind)
	}
}

func mouseButtonFlag(button int, down bool) (uint32, uint32, error) {
	switch button {
	case 0:
		if down {
			return MouseEventfLeftDown, 0, nil
		}
		return MouseEventfLeftUp, 0, nil
	case 1:
		if down {
			return MouseEventfMiddleDown, 0, nil
		}
		return MouseEventfMiddleUp, 0, nil
	case 2:
		if down {
			return MouseEventfRightDown, 0, nil
		}
		return MouseEventfRightUp, 0, nil
	case 3:
		if down {
			return MouseEventfXDown, XButton1, nil
		}
		return MouseEventfXUp, XButton1, nil
	case 4:
		if down {
			return MouseEventfXDown, XButton2, nil
		}
		return MouseEventfXUp, XButton2, nil
	default:
		return 0, 0, fmt.Errorf("%w: %d", ErrUnsupportedButton, button)
	}
}

func isExtendedVK(vk uint16) bool {
	switch vk {
	case 0x21, 0x22, 0x23, 0x24,
		0x25, 0x26, 0x27, 0x28,
		0x2C, 0x2D, 0x2E,
		0x5B, 0x5C, 0x5D,
		0x6F, 0x90,
		0xA3, 0xA5:
		return true
	default:
		return false
	}
}
