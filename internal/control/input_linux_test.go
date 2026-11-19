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

func TestLinuxInputActionClampsAbsoluteCoordinatesAtVirtualScreenEdges(t *testing.T) {
	tests := []struct {
		name  string
		dx    int32
		dy    int32
		wantX int
		wantY int
	}{
		{name: "top-left", dx: 0, dy: 0, wantX: 0, wantY: 0},
		{name: "bottom-right", dx: 65535, dy: 65535, wantX: 1919, wantY: 1079},
		{name: "negative", dx: -1, dy: -1, wantX: 0, wantY: 0},
		{name: "overflow", dx: 65536, dy: 65536, wantX: 1919, wantY: 1079},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			action, err := linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
				Dx: test.dx, Dy: test.dy, DwFlags: MouseEventfMove | MouseEventfAbsolute,
			}}, 1920, 1080)
			if err != nil {
				t.Fatalf("linuxInputAction returned error: %v", err)
			}
			if action.x != test.wantX || action.y != test.wantY {
				t.Fatalf("coordinates = (%d, %d), want (%d, %d)", action.x, action.y, test.wantX, test.wantY)
			}
		})
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

func TestLinuxInputActionMapsCommonKeyboardVKs(t *testing.T) {
	tests := []struct {
		vk     uint16
		keysym uint64
	}{
		{vk: 0x10, keysym: 0xffe1}, // Shift
		{vk: 0x11, keysym: 0xffe3}, // Control
		{vk: 0x12, keysym: 0xffe9}, // Alt
		{vk: 0x13, keysym: 0xff13}, // Pause
		{vk: 0x14, keysym: 0xffe5}, // Caps Lock
		{vk: 0x2d, keysym: 0xff63}, // Insert
		{vk: 0x2e, keysym: 0xffff}, // Delete
		{vk: 0x70, keysym: 0xffbe}, // F1
		{vk: 0x7b, keysym: 0xffc9}, // F12
		{vk: 0xa0, keysym: 0xffe1}, // Left Shift
		{vk: 0xa1, keysym: 0xffe2}, // Right Shift
		{vk: 0xa2, keysym: 0xffe3}, // Left Control
		{vk: 0xa4, keysym: 0xffe9}, // Left Alt
		{vk: 0xba, keysym: 0x3b},   // OEM 1
		{vk: 0xbb, keysym: 0x3d},   // OEM plus
		{vk: 0xbc, keysym: 0x2c},   // OEM comma
		{vk: 0xbd, keysym: 0x2d},   // OEM minus
		{vk: 0xbe, keysym: 0x2e},   // OEM period
		{vk: 0xbf, keysym: 0x2f},   // OEM 2
		{vk: 0xc0, keysym: 0x60},   // OEM 3
	}
	for _, test := range tests {
		action, err := linuxInputAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: test.vk}}, 0, 0)
		if err != nil {
			t.Fatalf("VK %#x returned error: %v", test.vk, err)
		}
		if action.keysym != test.keysym {
			t.Fatalf("VK %#x keysym = %#x, want %#x", test.vk, action.keysym, test.keysym)
		}
	}
}

func TestLinuxInputActionRejectsUnicodeKeyboardEvents(t *testing.T) {
	_, err := linuxInputAction(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{
		WVk: 0x41, DwFlags: KeyEventfUnicode,
	}}, 0, 0)
	if err == nil {
		t.Fatal("expected Unicode keyboard event to be rejected")
	}
}

func TestLinuxInputActionMapsWheelToX11Buttons(t *testing.T) {
	downWheelValue := int32(-120)
	upWheelValue := int32(120)
	downWheel := uint32(downWheelValue)
	upWheel := uint32(upWheelValue)
	action, err := linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: upWheel,
		DwFlags:   MouseEventfWheel,
	}}, 0, 0)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputButton || action.button != 4 || !action.down {
		t.Fatalf("unexpected wheel-up action: %+v", action)
	}

	action, err = linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: downWheel,
		DwFlags:   MouseEventfWheel,
	}}, 0, 0)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputButton || action.button != 5 || !action.down {
		t.Fatalf("unexpected wheel-down action: %+v", action)
	}
}

func TestLinuxInputActionMapsHorizontalWheel(t *testing.T) {
	action, err := linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: uint32(int32(120)), DwFlags: MouseEventfHWheel,
	}}, 0, 0)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputButton || action.button != 6 || !action.down {
		t.Fatalf("unexpected horizontal wheel action: %+v", action)
	}
}

func TestLinuxInputBackendRejectsUnsupportedInput(t *testing.T) {
	backend := &linuxInputBackend{}
	err := backend.SendInputs([]WinInput{{Type: 99}})
	if err == nil {
		t.Fatal("expected unsupported input error")
	}
}
