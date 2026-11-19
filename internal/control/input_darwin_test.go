//go:build darwin

package control

import (
	"errors"
	"testing"
)

func TestDarwinInputBackendRequiresNativeAccessibilitySupport(t *testing.T) {
	backend := newPlatformInputBackend()

	err := backend.SendInputs([]WinInput{{Type: InputTypeMouse}})
	if !errors.Is(err, ErrDarwinAccessibilityRequired) {
		t.Fatalf("SendInputs error = %v, want ErrDarwinAccessibilityRequired", err)
	}
	if !errors.Is(err, ErrDarwinInputUnavailable) {
		t.Fatalf("SendInputs error = %v, want ErrDarwinInputUnavailable", err)
	}
}

func TestDarwinInputBackendRejectsEmptyAndNonEmptyBatches(t *testing.T) {
	backend := newPlatformInputBackend()

	if err := backend.SendInputs(nil); !errors.Is(err, ErrDarwinAccessibilityRequired) {
		t.Fatalf("SendInputs(nil) error = %v, want accessibility error", err)
	}
	if err := backend.SendInputs([]WinInput{{Type: InputTypeKeyboard}}); !errors.Is(err, ErrDarwinInputUnavailable) {
		t.Fatalf("SendInputs(keyboard) error = %v, want unavailable error", err)
	}
}

func TestDarwinMouseActionMapsNormalizedCoordinatesAndButtons(t *testing.T) {
	action, err := darwinMouseAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		Dx: 32768, Dy: 16384, DwFlags: MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk,
	}}, 1920, 1080)
	if err != nil {
		t.Fatalf("darwinMouseAction move error = %v", err)
	}
	if action.kind != darwinMouseMove || action.x != 960 || action.y != 270 {
		t.Fatalf("unexpected move action: %#v", action)
	}

	action, err = darwinMouseAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		DwFlags: MouseEventfRightDown,
	}}, 1920, 1080)
	if err != nil {
		t.Fatalf("darwinMouseAction right down error = %v", err)
	}
	if action.kind != darwinMouseButtonKind || action.button != darwinButtonRight || !action.down {
		t.Fatalf("unexpected right button action: %#v", action)
	}
}

func TestDarwinMouseActionMapsWheelDirection(t *testing.T) {
	action, err := darwinMouseAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: 120, DwFlags: MouseEventfWheel,
	}}, 1920, 1080)
	if err != nil {
		t.Fatalf("darwinMouseAction wheel error = %v", err)
	}
	if action.kind != darwinMouseScroll || action.delta != 120 {
		t.Fatalf("unexpected positive wheel action: %#v", action)
	}

	negativeWheel := int32(-120)
	action, err = darwinMouseAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: uint32(negativeWheel), DwFlags: MouseEventfWheel,
	}}, 1920, 1080)
	if err != nil {
		t.Fatalf("darwinMouseAction negative wheel error = %v", err)
	}
	if action.delta != -120 {
		t.Fatalf("unexpected negative wheel delta: %#v", action)
	}
}

func TestDarwinKeyboardActionMapsCommonVirtualKeys(t *testing.T) {
	action, err := darwinKeyboardAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 'A'}})
	if err != nil {
		t.Fatalf("darwinKeyboardAction letter error = %v", err)
	}
	if action.keyCode != 0 || !action.down {
		t.Fatalf("unexpected letter action: %#v", action)
	}

	action, err = darwinKeyboardAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 0x0D, DwFlags: KeyEventfKeyUp}})
	if err != nil {
		t.Fatalf("darwinKeyboardAction enter error = %v", err)
	}
	if action.keyCode != 36 || action.down {
		t.Fatalf("unexpected enter action: %#v", action)
	}

	if _, err := darwinKeyboardAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 0xFFFF}}); !errors.Is(err, ErrDarwinUnsupportedKey) {
		t.Fatalf("unsupported key error = %v, want ErrDarwinUnsupportedKey", err)
	}
}
