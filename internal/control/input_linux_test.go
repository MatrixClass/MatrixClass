//go:build linux

package control

import (
	"errors"
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
	if action.kind != linuxInputClick || action.button != 4 {
		t.Fatalf("unexpected wheel-up action: %+v", action)
	}

	action, err = linuxInputAction(WinInput{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: downWheel,
		DwFlags:   MouseEventfWheel,
	}}, 0, 0)
	if err != nil {
		t.Fatalf("linuxInputAction returned error: %v", err)
	}
	if action.kind != linuxInputClick || action.button != 5 {
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
	if action.kind != linuxInputClick || action.button != 6 {
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

func TestLinuxInputCloseReleasesBothLibrariesWhenFirstCloseFails(t *testing.T) {
	var closed []uintptr
	errFirst := errors.New("x11 close failed")
	err := releaseLinuxInputLibraries(11, 22, func(handle uintptr) error {
		closed = append(closed, handle)
		if handle == 11 {
			return errFirst
		}
		return nil
	})
	if !errors.Is(err, errFirst) {
		t.Fatalf("releaseLinuxInputLibraries error = %v, want %v", err, errFirst)
	}
	if len(closed) != 2 || closed[0] != 11 || closed[1] != 22 {
		t.Fatalf("closed handles = %#v, want [11 22]", closed)
	}
}

func TestLinuxInputCloseIsIdempotent(t *testing.T) {
	var closed []uintptr
	backend := &linuxInputBackend{handle: 11, xtstHandle: 22, close: func(uintptr) int32 { return 1 }}
	backend.closeLibraries = func(handle uintptr) error {
		closed = append(closed, handle)
		return nil
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if len(closed) != 2 || closed[0] != 11 || closed[1] != 22 {
		t.Fatalf("closed handles = %#v, want [11 22]", closed)
	}
}

func TestLinuxInputBackendWheelPressesAndReleases(t *testing.T) {
	type buttonCall struct {
		button uint32
		down   int32
	}
	var calls []buttonCall
	backend := &linuxInputBackend{
		display: 1,
		width:   100,
		height:  100,
		button: func(_ uintptr, button uint32, down int32, _ uint64) int32 {
			calls = append(calls, buttonCall{button, down})
			return 1
		},
		flush: func(uintptr) int32 { return 1 },
	}
	wheelDown := int32(-120)
	err := backend.SendInputs([]WinInput{{Type: InputTypeMouse, Mi: MouseInput{
		MouseData: uint32(wheelDown), DwFlags: MouseEventfWheel,
	}}})
	if err != nil {
		t.Fatalf("SendInputs: %v", err)
	}
	if len(calls) != 2 || calls[0] != (buttonCall{5, 1}) || calls[1] != (buttonCall{5, 0}) {
		t.Fatalf("wheel must press and release button 5, got %+v", calls)
	}
}

func TestLinuxVKToKeysymSnapshotHelpAndPrior(t *testing.T) {
	for vk, want := range map[uint16]uint64{0x2c: 0xff61, 0x2f: 0xff6a, 0x21: 0xff55, 0x22: 0xff56} {
		got, ok := linuxVKToKeysym(vk)
		if !ok || got != want {
			t.Fatalf("linuxVKToKeysym(%#x) = %#x,%v want %#x", vk, got, ok, want)
		}
	}
}
