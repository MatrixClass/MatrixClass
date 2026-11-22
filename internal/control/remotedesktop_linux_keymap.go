package control

// Pure, OS-independent logic for the Linux Wayland xdg-desktop-portal
// RemoteDesktop input backend (remotedesktop_linux.go). The file name suffix
// "_keymap" is not a GOOS tag, so these helpers compile and are unit-tested on
// every platform.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Linux evdev button codes (linux/input-event-codes.h).
const (
	evdevBtnLeft   int32 = 0x110
	evdevBtnRight  int32 = 0x111
	evdevBtnMiddle int32 = 0x112
	evdevBtnSide   int32 = 0x113
	evdevBtnExtra  int32 = 0x114
)

// Portal device-type bits (org.freedesktop.portal.RemoteDesktop).
const (
	rdDeviceKeyboard uint32 = 1
	rdDevicePointer  uint32 = 2
)

// Portal axis values for NotifyPointerAxisDiscrete.
const (
	rdAxisVertical   uint32 = 0
	rdAxisHorizontal uint32 = 1
)

// Portal persist_mode: 2 = persist until explicitly revoked.
const rdPersistUntilRevoked uint32 = 2

const (
	rdRestoreTokenDir  = "matrixclass"
	rdRestoreTokenFile = "remote-desktop-restore-token"
)

type rdActionKind int

const (
	rdPointerAbsolute rdActionKind = iota + 1
	rdPointerButton
	rdPointerAxis
	rdKeyboardKey
)

// rdAction is one portal Notify* call. Absolute positions are normalized to
// [0,1] and mapped to stream coordinates when sent.
type rdAction struct {
	kind    rdActionKind
	x, y    float64
	button  int32
	pressed bool
	axis    uint32
	steps   int32
	keycode int32
}

// rdActionsFromInput translates one Windows-style SendInput record into the
// portal calls needed to reproduce it.
func rdActionsFromInput(input WinInput) ([]rdAction, error) {
	switch input.Type {
	case InputTypeMouse:
		mi := input.Mi
		flags := mi.DwFlags
		if flags&MouseEventfWheel != 0 {
			return []rdAction{{kind: rdPointerAxis, axis: rdAxisVertical, steps: rdWheelSteps(mi.MouseData, false)}}, nil
		}
		if flags&MouseEventfHWheel != 0 {
			return []rdAction{{kind: rdPointerAxis, axis: rdAxisHorizontal, steps: rdWheelSteps(mi.MouseData, true)}}, nil
		}
		var actions []rdAction
		if flags&MouseEventfMove != 0 {
			if flags&MouseEventfAbsolute == 0 {
				return nil, errors.New("control: relative mouse motion is not supported for Wayland remote input")
			}
			actions = append(actions, rdAction{kind: rdPointerAbsolute, x: rdNormalize(mi.Dx), y: rdNormalize(mi.Dy)})
		}
		if button, pressed, ok := rdMouseButton(flags, mi.MouseData); ok {
			actions = append(actions, rdAction{kind: rdPointerButton, button: button, pressed: pressed})
		} else if flags&(MouseEventfXDown|MouseEventfXUp) != 0 {
			return nil, fmt.Errorf("%w: X button %d", ErrUnsupportedButton, mi.MouseData)
		}
		if len(actions) == 0 {
			return nil, errors.New("control: unsupported Wayland mouse event")
		}
		return actions, nil
	case InputTypeKeyboard:
		ki := input.Ki
		if ki.DwFlags&KeyEventfUnicode != 0 {
			return nil, errors.New("control: Unicode keyboard input is unavailable through the Wayland portal")
		}
		code, ok := rdVKToEvdev(ki.WVk, ki.DwFlags&KeyEventfExtendedKey != 0)
		if !ok {
			return nil, fmt.Errorf("control: unsupported virtual key %#x on Wayland", ki.WVk)
		}
		return []rdAction{{kind: rdKeyboardKey, keycode: code, pressed: ki.DwFlags&KeyEventfKeyUp == 0}}, nil
	default:
		return nil, ErrInvalidInputKind
	}
}

func rdNormalize(value int32) float64 {
	if value <= 0 {
		return 0
	}
	if value >= 65535 {
		return 1
	}
	return float64(value) / 65535
}

// rdStreamCoordinate maps a normalized coordinate into a stream of the given
// logical extent, keeping the result strictly inside [0, extent).
func rdStreamCoordinate(norm float64, extent int32) float64 {
	if extent <= 1 || norm <= 0 || norm != norm {
		return 0
	}
	if norm >= 1 {
		return float64(extent - 1)
	}
	return norm * float64(extent-1)
}

// rdWheelSteps converts a Windows wheel delta (multiples of WHEEL_DELTA=120,
// positive = away from the user / right) into portal discrete steps, where
// positive vertical steps scroll down (libinput convention). Partial deltas
// still produce one step so high-resolution wheels are not swallowed.
func rdWheelSteps(data uint32, horizontal bool) int32 {
	delta := int32(data)
	if delta == 0 {
		return 0
	}
	steps := delta / 120
	if steps == 0 {
		if delta > 0 {
			steps = 1
		} else {
			steps = -1
		}
	}
	if !horizontal {
		steps = -steps
	}
	return steps
}

func rdMouseButton(flags, data uint32) (int32, bool, bool) {
	switch {
	case flags&MouseEventfLeftDown != 0:
		return evdevBtnLeft, true, true
	case flags&MouseEventfLeftUp != 0:
		return evdevBtnLeft, false, true
	case flags&MouseEventfRightDown != 0:
		return evdevBtnRight, true, true
	case flags&MouseEventfRightUp != 0:
		return evdevBtnRight, false, true
	case flags&MouseEventfMiddleDown != 0:
		return evdevBtnMiddle, true, true
	case flags&MouseEventfMiddleUp != 0:
		return evdevBtnMiddle, false, true
	case flags&(MouseEventfXDown|MouseEventfXUp) != 0:
		pressed := flags&MouseEventfXDown != 0
		switch data {
		case XButton1:
			return evdevBtnSide, pressed, true
		case XButton2:
			return evdevBtnExtra, pressed, true
		}
	}
	return 0, false, false
}

// rdVKToEvdev maps a Windows virtual-key code to a Linux evdev keycode
// (linux/input-event-codes.h). Keycodes are layout independent, which keeps
// shortcuts such as Ctrl+C working regardless of the student's keyboard
// layout. extended distinguishes numpad Enter (VK_RETURN + extended flag).
func rdVKToEvdev(vk uint16, extended bool) (int32, bool) {
	switch {
	case vk >= 'A' && vk <= 'Z':
		return rdLetterKeycodes[vk-'A'], true
	case vk >= '1' && vk <= '9':
		return int32(2 + vk - '1'), true // KEY_1..KEY_9
	case vk == '0':
		return 11, true
	case vk >= 0x70 && vk <= 0x79: // F1..F10
		return int32(59 + vk - 0x70), true
	case vk == 0x7a: // F11
		return 87, true
	case vk == 0x7b: // F12
		return 88, true
	case vk >= 0x7c && vk <= 0x87: // F13..F24
		return int32(183 + vk - 0x7c), true
	}
	if vk == 0x0d && extended {
		return 96, true // KEY_KPENTER
	}
	code, ok := rdVKKeycodes[vk]
	return code, ok
}

// KEY_A..KEY_Z follow the QWERTY scan-code layout, not alphabetical order.
var rdLetterKeycodes = [26]int32{
	30, 48, 46, 32, 18, 33, 34, 35, 23, 36, 37, 38, 50, // A..M
	49, 24, 25, 16, 19, 31, 20, 22, 47, 17, 45, 21, 44, // N..Z
}

var rdVKKeycodes = map[uint16]int32{
	0x08: 14,    // VK_BACK -> KEY_BACKSPACE
	0x09: 15,    // VK_TAB
	0x0c: 76,    // VK_CLEAR -> KEY_KP5
	0x0d: 28,    // VK_RETURN -> KEY_ENTER
	0x10: 42,    // VK_SHIFT -> KEY_LEFTSHIFT
	0x11: 29,    // VK_CONTROL -> KEY_LEFTCTRL
	0x12: 56,    // VK_MENU -> KEY_LEFTALT
	0x13: 119,   // VK_PAUSE
	0x14: 58,    // VK_CAPITAL -> KEY_CAPSLOCK
	0x15: 122,   // VK_HANGUL/KANA -> KEY_HANGEUL
	0x19: 123,   // VK_HANJA
	0x1b: 1,     // VK_ESCAPE
	0x1c: 92,    // VK_CONVERT -> KEY_HENKAN
	0x1d: 94,    // VK_NONCONVERT -> KEY_MUHENKAN
	0x20: 57,    // VK_SPACE
	0x21: 104,   // VK_PRIOR -> KEY_PAGEUP
	0x22: 109,   // VK_NEXT -> KEY_PAGEDOWN
	0x23: 107,   // VK_END
	0x24: 102,   // VK_HOME
	0x25: 105,   // VK_LEFT
	0x26: 103,   // VK_UP
	0x27: 106,   // VK_RIGHT
	0x28: 108,   // VK_DOWN
	0x29: 0x161, // VK_SELECT -> KEY_SELECT
	0x2a: 210,   // VK_PRINT -> KEY_PRINT
	0x2b: 96,    // VK_EXECUTE -> KEY_KPENTER (mirrors the X11 map)
	0x2c: 99,    // VK_SNAPSHOT -> KEY_SYSRQ (Print Screen)
	0x2d: 110,   // VK_INSERT
	0x2e: 111,   // VK_DELETE
	0x2f: 138,   // VK_HELP
	0x5b: 125,   // VK_LWIN -> KEY_LEFTMETA
	0x5c: 126,   // VK_RWIN -> KEY_RIGHTMETA
	0x5d: 127,   // VK_APPS -> KEY_COMPOSE (context menu key)
	0x5f: 142,   // VK_SLEEP
	0x60: 82,    // VK_NUMPAD0
	0x61: 79,    // VK_NUMPAD1
	0x62: 80,
	0x63: 81,
	0x64: 75,
	0x65: 76,
	0x66: 77,
	0x67: 71,
	0x68: 72,
	0x69: 73,  // VK_NUMPAD9
	0x6a: 55,  // VK_MULTIPLY -> KEY_KPASTERISK
	0x6b: 78,  // VK_ADD -> KEY_KPPLUS
	0x6c: 121, // VK_SEPARATOR -> KEY_KPCOMMA
	0x6d: 74,  // VK_SUBTRACT -> KEY_KPMINUS
	0x6e: 83,  // VK_DECIMAL -> KEY_KPDOT
	0x6f: 98,  // VK_DIVIDE -> KEY_KPSLASH
	0x90: 69,  // VK_NUMLOCK
	0x91: 70,  // VK_SCROLL -> KEY_SCROLLLOCK
	0xa0: 42,  // VK_LSHIFT
	0xa1: 54,  // VK_RSHIFT
	0xa2: 29,  // VK_LCONTROL
	0xa3: 97,  // VK_RCONTROL
	0xa4: 56,  // VK_LMENU
	0xa5: 100, // VK_RMENU -> KEY_RIGHTALT
	0xa6: 158, // VK_BROWSER_BACK -> KEY_BACK
	0xa7: 159, // VK_BROWSER_FORWARD
	0xa8: 173, // VK_BROWSER_REFRESH
	0xa9: 128, // VK_BROWSER_STOP -> KEY_STOP
	0xaa: 217, // VK_BROWSER_SEARCH
	0xab: 156, // VK_BROWSER_FAVORITES -> KEY_BOOKMARKS
	0xac: 172, // VK_BROWSER_HOME -> KEY_HOMEPAGE
	0xad: 113, // VK_VOLUME_MUTE
	0xae: 114, // VK_VOLUME_DOWN
	0xaf: 115, // VK_VOLUME_UP
	0xb0: 163, // VK_MEDIA_NEXT_TRACK -> KEY_NEXTSONG
	0xb1: 165, // VK_MEDIA_PREV_TRACK -> KEY_PREVIOUSSONG
	0xb2: 166, // VK_MEDIA_STOP -> KEY_STOPCD
	0xb3: 164, // VK_MEDIA_PLAY_PAUSE
	0xb4: 155, // VK_LAUNCH_MAIL -> KEY_MAIL
	0xba: 39,  // VK_OEM_1 ;:
	0xbb: 13,  // VK_OEM_PLUS =+
	0xbc: 51,  // VK_OEM_COMMA
	0xbd: 12,  // VK_OEM_MINUS
	0xbe: 52,  // VK_OEM_PERIOD
	0xbf: 53,  // VK_OEM_2 /?
	0xc0: 41,  // VK_OEM_3 `~
	0xdb: 26,  // VK_OEM_4 [{
	0xdc: 43,  // VK_OEM_5 \|
	0xdd: 27,  // VK_OEM_6 ]}
	0xde: 40,  // VK_OEM_7 '"
	0xdf: 43,  // VK_OEM_8 -> KEY_BACKSLASH (mirrors the X11 map)
	0xe2: 86,  // VK_OEM_102 -> KEY_102ND
}

// linuxSessionIsWayland reports whether the desktop session is Wayland.
// XDG_SESSION_TYPE is authoritative when set; otherwise WAYLAND_DISPLAY is
// used. An X11 session that merely exports WAYLAND_DISPLAY (nested
// compositors) stays on the X11 path.
func linuxSessionIsWayland(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("XDG_SESSION_TYPE"))) {
	case "wayland":
		return true
	case "x11", "tty", "mir":
		return false
	}
	return strings.TrimSpace(getenv("WAYLAND_DISPLAY")) != ""
}

// rdRestoreTokenPath returns $XDG_CONFIG_HOME/matrixclass/<file>, falling back
// to $HOME/.config when XDG_CONFIG_HOME is unset or not absolute.
func rdRestoreTokenPath(getenv func(string) string) (string, error) {
	base := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if base == "" || !strings.HasPrefix(base, "/") {
		home := strings.TrimSpace(getenv("HOME"))
		if home == "" {
			return "", errors.New("control: cannot locate config directory for the portal restore token")
		}
		base = strings.TrimRight(home, "/") + "/.config"
	}
	return filepath.ToSlash(filepath.Join(base, rdRestoreTokenDir, rdRestoreTokenFile)), nil
}

// rdRequestPath predicts the portal Request object path for a handle_token, as
// specified by org.freedesktop.portal.Request: the sender's unique name
// without the leading ':' and with '.' replaced by '_'.
func rdRequestPath(uniqueName, token string) string {
	sender := strings.ReplaceAll(strings.TrimPrefix(uniqueName, ":"), ".", "_")
	return "/org/freedesktop/portal/desktop/request/" + sender + "/" + token
}

// rdResponseError maps a portal Request.Response code to an error.
func rdResponseError(code uint32, stage string) error {
	switch code {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%w (%s cancelled by user)", ErrWaylandRemoteInputDenied, stage)
	default:
		return fmt.Errorf("control: Wayland portal %s failed (response %d)", stage, code)
	}
}

// rdValidToken reports whether a restore token is safe to pass back to the
// portal (portal tokens are UUID-like ASCII strings).
func rdValidToken(token string) bool {
	if token == "" || len(token) > 256 {
		return false
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
