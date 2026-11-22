//go:build darwin

package media

import (
	"errors"
	"testing"

	"nexlab/internal/core"
)

func TestDarwinAudioLoopbackReportsClearError(t *testing.T) {
	audio := NewAudioEngine()
	defer audio.Close()
	err := audio.StartCapture(true, func(*core.AudioFrame) {})
	if !errors.Is(err, ErrDarwinAudioLoopbackUnavailable) && !errors.Is(err, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("loopback capture error = %v", err)
	}
}

func TestDarwinAudioCapabilityPermissionMapping(t *testing.T) {
	if err := darwinAudioCapabilityPermission(darwinAudioAuthAuthorized); err != nil {
		t.Fatalf("authorized must report nil, got %v", err)
	}
	for _, s := range []darwinAudioAuthStatus{darwinAudioAuthNotDetermined, darwinAudioAuthDenied, darwinAudioAuthRestricted, darwinAudioAuthUnknown} {
		if err := darwinAudioCapabilityPermission(s); !errors.Is(err, ErrDarwinCoreAudioPermission) {
			t.Fatalf("%s must report permission error, got %v", s, err)
		}
	}
}

func TestDarwinAudioCapabilitiesMatchNativeAvailability(t *testing.T) {
	status := DarwinMediaCapabilities()
	native := newDarwinAudioAQNative()
	if err := native.Available(); err != nil {
		if status.CoreAudioAvailable || !errors.Is(status.CoreAudioError, ErrDarwinCoreAudioUnavailable) {
			t.Fatalf("unexpected status without frameworks: %+v", status)
		}
		return
	}
	if !status.CoreAudioAvailable || status.CoreAudioError != nil {
		t.Fatalf("frameworks loaded but status = %+v", status)
	}
	if _, ok := newDarwinDefaultAudioBridge().(*darwinAudioQueueBridge); !ok {
		t.Fatal("expected AudioQueue bridge when frameworks are available")
	}
}

func TestDarwinAudioErrorsAreDistinct(t *testing.T) {
	errs := darwinAudioErrors()
	all := []error{errs.Unavailable, errs.Permission, errs.Device, errs.Loopback, errs.Closed}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Fatalf("errors %d and %d alias", i, j)
			}
		}
	}
}
