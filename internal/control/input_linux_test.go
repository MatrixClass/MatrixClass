//go:build linux

package control

import (
	"testing"
)

func TestLinuxInputActionMapsNormalizedMouseEvents(t *testing.T) {
	action, err := linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		Dx:      32768,
		Dy:      16384,
		DwFlags: MouseEventfMove | MouseEventfAbsolute | MouseEventfVirtualDesk | MouseEventfLeftDown,
	}}, 1920, 1080)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputMotionButton || action.x != 960 || action.y != 270 || action.button != 1 || !action.down {
		t.Fatalf("unexpected action: %+v", action)
	}
}

func TestLinuxInputActionMapsKeyboardVK(t *testing.T) {
	action, err := linuxInputAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 0x41}}, 0, 0)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputKey || action.keysym != 0x61 || action.down != true {
		t.Fatalf("unexpected keyboard action: %+v", action)
	}
}

func TestLinuxInputBackendRejectsUnsupportedInput(t *testing.T) {
	backend := &linuxInputBackend{}
	err := backend.SendInputs([]WinInput{{Type: 99}})
	if err == nil {
		t.Fatal("expected unsupported input error")
	}
}
