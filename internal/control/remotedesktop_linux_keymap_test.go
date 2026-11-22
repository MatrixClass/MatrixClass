package control

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"nexlab/internal/core"
)

func TestRDVKToEvdevLettersDigitsAndFunctionKeys(t *testing.T) {
	cases := map[uint16]int32{
		'A': 30, 'C': 46, 'Q': 16, 'V': 47, 'Z': 44, 'M': 50, 'N': 49,
		'1': 2, '9': 10, '0': 11,
		0x70: 59, 0x79: 68, 0x7a: 87, 0x7b: 88, 0x7c: 183, 0x87: 194,
	}
	for vk, want := range cases {
		got, ok := rdVKToEvdev(vk, false)
		if !ok || got != want {
			t.Errorf("rdVKToEvdev(%#x) = %d,%v want %d", vk, got, ok, want)
		}
	}
}

func TestRDVKToEvdevCoversEveryX11MappedKey(t *testing.T) {
	// Every VK the X11 backend accepts must also work on Wayland.
	for vk := uint16(1); vk < 0x100; vk++ {
		if !x11MappedVK(vk) {
			continue
		}
		if _, ok := rdVKToEvdev(vk, false); !ok {
			t.Errorf("VK %#x is mapped on X11 but not on Wayland", vk)
		}
	}
}

// x11MappedVK mirrors the VK set of linuxVKToKeysym (input_linux.go), which
// is only compiled on Linux.
func x11MappedVK(vk uint16) bool {
	switch {
	case vk >= 'A' && vk <= 'Z', vk >= '0' && vk <= '9', vk >= 0x60 && vk <= 0x6f && vk != 0x6c, vk >= 0x70 && vk <= 0x87:
		return true
	}
	switch vk {
	case 0x08, 0x09, 0x0d, 0x1b, 0x20, 0x10, 0xa0, 0xa1, 0x11, 0xa2, 0xa3, 0x12, 0xa4, 0xa5,
		0x13, 0x14, 0x15, 0x2c, 0x2d, 0x2e, 0x2f, 0x21, 0x22, 0x23, 0x24, 0x5b, 0x5c, 0x5d,
		0x90, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf, 0xc0, 0xdb, 0xdc, 0xdd, 0xde, 0x25, 0x26,
		0x27, 0x28, 0x6c, 0x91, 0xad, 0xae, 0xaf, 0xb0, 0xb1, 0xb2, 0xb3, 0xe2, 0xdf, 0x2b,
		0x0c, 0x19, 0x1c, 0x1d, 0x29, 0x2a, 0x5f, 0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xb4:
		return true
	}
	return false
}

func TestRDVKToEvdevSpecialKeys(t *testing.T) {
	cases := []struct {
		vk       uint16
		extended bool
		want     int32
	}{
		{0x0d, false, 28},  // Enter
		{0x0d, true, 96},   // numpad Enter
		{0x2c, false, 99},  // Print Screen -> KEY_SYSRQ
		{0x2f, false, 138}, // Help
		{0x21, false, 104}, // Page Up
		{0x5b, false, 125}, // Left Win -> KEY_LEFTMETA
		{0xa5, false, 100}, // Right Alt
		{0xa3, false, 97},  // Right Ctrl
		{0x1b, false, 1},   // Esc
		{0x2e, false, 111}, // Delete
		{0xc0, false, 41},  // `
		{0xad, false, 113}, // mute
	}
	for _, tc := range cases {
		got, ok := rdVKToEvdev(tc.vk, tc.extended)
		if !ok || got != tc.want {
			t.Errorf("rdVKToEvdev(%#x, %v) = %d,%v want %d", tc.vk, tc.extended, got, ok, tc.want)
		}
	}
	if _, ok := rdVKToEvdev(0xff, false); ok {
		t.Fatal("unknown VK 0xff unexpectedly mapped")
	}
}

func TestRDActionsFromBuiltInputs(t *testing.T) {
	inputs, err := BuildInputs(core.RemoteInputEvent{Kind: core.InputMouseDown, Button: 0, X: 0.5, Y: 1})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := rdActionsFromInput(inputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].kind != rdPointerAbsolute || actions[1].kind != rdPointerButton {
		t.Fatalf("unexpected actions %+v", actions)
	}
	if actions[0].y != 1 || actions[0].x < 0.49 || actions[0].x > 0.51 {
		t.Fatalf("unexpected position %+v", actions[0])
	}
	if actions[1].button != evdevBtnLeft || !actions[1].pressed {
		t.Fatalf("unexpected button %+v", actions[1])
	}

	for button, want := range map[int]int32{1: evdevBtnMiddle, 2: evdevBtnRight, 3: evdevBtnSide, 4: evdevBtnExtra} {
		inputs, err := BuildInputs(core.RemoteInputEvent{Kind: core.InputMouseUp, Button: button, X: -1, Y: -1})
		if err != nil {
			t.Fatal(err)
		}
		actions, err := rdActionsFromInput(inputs[0])
		if err != nil || len(actions) != 1 || actions[0].button != want || actions[0].pressed {
			t.Fatalf("button %d: actions=%+v err=%v", button, actions, err)
		}
	}

	inputs, _ = BuildInputs(core.RemoteInputEvent{Kind: core.InputKeyUp, VKCode: 'C'})
	actions, err = rdActionsFromInput(inputs[0])
	if err != nil || len(actions) != 1 || actions[0].kind != rdKeyboardKey || actions[0].keycode != 46 || actions[0].pressed {
		t.Fatalf("key up: actions=%+v err=%v", actions, err)
	}
}

func TestRDWheelSteps(t *testing.T) {
	cases := []struct {
		delta      int32
		horizontal bool
		want       int32
	}{
		{120, false, -1}, // wheel up -> scroll up (negative libinput steps)
		{-120, false, 1},
		{240, false, -2},
		{30, false, -1}, // high-resolution partial notch still scrolls
		{-30, false, 1},
		{120, true, 1}, // tilt right
		{-240, true, -2},
		{0, false, 0},
	}
	for _, tc := range cases {
		if got := rdWheelSteps(uint32(tc.delta), tc.horizontal); got != tc.want {
			t.Errorf("rdWheelSteps(%d,%v) = %d want %d", tc.delta, tc.horizontal, got, tc.want)
		}
	}
	inputs, _ := BuildInputs(core.RemoteInputEvent{Kind: core.InputMouseWheel, Delta: -120})
	actions, err := rdActionsFromInput(inputs[0])
	if err != nil || len(actions) != 1 || actions[0].kind != rdPointerAxis || actions[0].axis != rdAxisVertical || actions[0].steps != 1 {
		t.Fatalf("wheel actions=%+v err=%v", actions, err)
	}
}

func TestRDActionsRejectUnsupportedInput(t *testing.T) {
	if _, err := rdActionsFromInput(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 'A', DwFlags: KeyEventfUnicode}}); err == nil {
		t.Fatal("Unicode input accepted")
	}
	if _, err := rdActionsFromInput(WinInput{Type: InputTypeMouse, Mi: MouseInput{DwFlags: MouseEventfMove, Dx: 5}}); err == nil {
		t.Fatal("relative motion accepted")
	}
	if _, err := rdActionsFromInput(WinInput{Type: InputTypeMouse, Mi: MouseInput{DwFlags: MouseEventfXDown, MouseData: 7}}); !errors.Is(err, ErrUnsupportedButton) {
		t.Fatalf("unknown X button err = %v", err)
	}
	if _, err := rdActionsFromInput(WinInput{Type: 9}); !errors.Is(err, ErrInvalidInputKind) {
		t.Fatalf("bad kind err = %v", err)
	}
}

func TestRDStreamCoordinate(t *testing.T) {
	if got := rdStreamCoordinate(0, 1920); got != 0 {
		t.Fatalf("0 -> %v", got)
	}
	if got := rdStreamCoordinate(1, 1920); got != 1919 {
		t.Fatalf("1 -> %v", got)
	}
	if got := rdStreamCoordinate(0.5, 1921); got != 960 {
		t.Fatalf("0.5 -> %v", got)
	}
	if got := rdStreamCoordinate(0.5, 0); got != 0 {
		t.Fatalf("zero extent -> %v", got)
	}
}

func TestLinuxSessionIsWayland(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		values map[string]string
		want   bool
	}{
		{map[string]string{"XDG_SESSION_TYPE": "wayland"}, true},
		{map[string]string{"XDG_SESSION_TYPE": "Wayland", "DISPLAY": ":0"}, true},
		{map[string]string{"XDG_SESSION_TYPE": "x11", "WAYLAND_DISPLAY": "wayland-0"}, false},
		{map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{map[string]string{"DISPLAY": ":0"}, false},
		{map[string]string{}, false},
	}
	for _, tc := range cases {
		if got := linuxSessionIsWayland(env(tc.values)); got != tc.want {
			t.Errorf("linuxSessionIsWayland(%v) = %v want %v", tc.values, got, tc.want)
		}
	}
}

func TestRDRestoreTokenPath(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	got, err := rdRestoreTokenPath(env(map[string]string{"XDG_CONFIG_HOME": "/home/s/.cfg", "HOME": "/home/s"}))
	if err != nil || got != "/home/s/.cfg/matrixclass/remote-desktop-restore-token" {
		t.Fatalf("path=%q err=%v", got, err)
	}
	got, err = rdRestoreTokenPath(env(map[string]string{"XDG_CONFIG_HOME": "relative", "HOME": "/home/s/"}))
	if err != nil || got != "/home/s/.config/matrixclass/remote-desktop-restore-token" {
		t.Fatalf("fallback path=%q err=%v", got, err)
	}
	if _, err := rdRestoreTokenPath(env(map[string]string{})); err == nil {
		t.Fatal("expected error without HOME")
	}
}

func TestRDRequestPathAndResponses(t *testing.T) {
	if got := rdRequestPath(":1.42", "tok"); got != "/org/freedesktop/portal/desktop/request/1_42/tok" {
		t.Fatalf("request path %q", got)
	}
	if err := rdResponseError(0, "Start"); err != nil {
		t.Fatal(err)
	}
	if err := rdResponseError(1, "Start"); !errors.Is(err, ErrWaylandRemoteInputDenied) {
		t.Fatalf("cancel err = %v", err)
	}
	if err := rdResponseError(2, "Start"); err == nil || errors.Is(err, ErrWaylandRemoteInputDenied) {
		t.Fatalf("other err = %v", err)
	}
	if rdValidToken("") || rdValidToken("a b") || rdValidToken("x\n") || !rdValidToken("3f2a-11ee-b962") {
		t.Fatal("rdValidToken misclassified tokens")
	}
}

func TestRDParseStartResults(t *testing.T) {
	streams := []struct {
		NodeID uint32
		Props  map[string]dbus.Variant
	}{{NodeID: 57, Props: map[string]dbus.Variant{"size": dbus.MakeVariant([]any{int32(1920), int32(1080)})}}}
	results := map[string]dbus.Variant{
		"devices":       dbus.MakeVariant(uint32(3)),
		"restore_token": dbus.MakeVariant("abc-123"),
		"streams":       dbus.MakeVariant(streams),
	}
	info := rdParseStartResults(results)
	if info.devices != 3 || info.restoreToken != "abc-123" || !info.hasStream || info.stream != 57 || info.width != 1920 || info.height != 1080 {
		t.Fatalf("unexpected info %+v", info)
	}

	loose := [][]any{{uint32(9), map[string]dbus.Variant{"size": dbus.MakeVariant([]any{int32(800), int32(600)})}}}
	node, w, h, ok := rdParseFirstStreamLoose(loose)
	if !ok || node != 9 || w != 800 || h != 600 {
		t.Fatalf("loose parse = %d %d %d %v", node, w, h, ok)
	}
	info = rdParseStartResults(map[string]dbus.Variant{"devices": dbus.MakeVariant(uint32(1)), "restore_token": dbus.MakeVariant("bad token")})
	if info.hasStream || info.restoreToken != "" || info.devices != 1 {
		t.Fatalf("unexpected info without streams %+v", info)
	}
}

func TestRDParseResponseAndSessionHandle(t *testing.T) {
	code, results, ok := rdParseResponse([]any{uint32(0), map[string]dbus.Variant{"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/1_42/s")}})
	if !ok || code != 0 {
		t.Fatalf("parse response code=%d ok=%v", code, ok)
	}
	if handle, ok := rdSessionHandle(results); !ok || handle != "/org/freedesktop/portal/desktop/session/1_42/s" {
		t.Fatalf("handle=%q ok=%v", handle, ok)
	}
	if _, ok := rdSessionHandle(map[string]dbus.Variant{"session_handle": dbus.MakeVariant("not a path")}); ok {
		t.Fatal("invalid handle accepted")
	}
	if _, _, ok := rdParseResponse([]any{"x"}); ok {
		t.Fatal("malformed response accepted")
	}
}
