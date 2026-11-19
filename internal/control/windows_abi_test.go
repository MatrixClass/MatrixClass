//go:build windows

package control

import "testing"

func TestWindowsInputEmptyAndRawMapping(t *testing.T) {
	backend := &windowsInputBackend{}
	if err := backend.SendInputs(nil); err != nil {
		t.Fatalf("empty input returned error: %v", err)
	}
	raw := toRawWin32Input(WinInput{Type: InputTypeKeyboard, Ki: KeybdInput{WVk: 'A'}})
	if raw.inputType != InputTypeKeyboard {
		t.Fatalf("raw input type = %d", raw.inputType)
	}
}

func TestWindowsLockKeyGuardRejectsNullHookData(t *testing.T) {
	if shouldBlockLockedKey(wmKeyDown, 0) {
		t.Fatal("nil low-level keyboard hook data must not be blocked")
	}
}
