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
