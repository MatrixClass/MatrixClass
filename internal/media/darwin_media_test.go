//go:build darwin

package media

import (
	"errors"
	"testing"

	"nexlab/internal/core"
)

func TestDarwinScreenCaptureDoesNotPretendToBeNative(t *testing.T) {
	capturer := NewScreenCapturer(CaptureOptions{Width: 320, Height: 180})
	defer capturer.Close()

	if capturer.Backend() != CaptureBackendUnavailable {
		t.Fatalf("expected unavailable backend without native bridge, got %q", capturer.Backend())
	}
	if _, err := capturer.CaptureScreen(); !errors.Is(err, ErrDarwinScreenCaptureUnavailable) {
		t.Fatalf("expected Darwin ScreenCaptureKit error, got %v", err)
	}
}

func TestDarwinSyntheticCaptureRequiresExplicitOptIn(t *testing.T) {
	capturer := NewScreenCapturer(CaptureOptions{Width: 16, Height: 16, ForceSynthetic: true})
	defer capturer.Close()

	if capturer.Backend() != CaptureBackendSynthetic {
		t.Fatalf("expected synthetic backend after explicit opt-in, got %q", capturer.Backend())
	}
	if _, err := capturer.CaptureScreen(); err != nil {
		t.Fatalf("explicit synthetic capture failed: %v", err)
	}
}

func TestDarwinMediaCapabilitiesExposeMissingNativePermissions(t *testing.T) {
	status := DarwinMediaCapabilities()
	if status.ScreenCaptureAvailable || !errors.Is(status.ScreenCaptureError, ErrDarwinScreenCaptureUnavailable) {
		t.Fatalf("unexpected screen capability status: %+v", status)
	}
	if status.CoreAudioAvailable || !errors.Is(status.CoreAudioError, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("unexpected CoreAudio capability status: %+v", status)
	}
}

func TestDarwinAudioReportsUnavailableInsteadOfGeneratingSilence(t *testing.T) {
	audio := NewAudioEngine()
	defer audio.Close()

	if err := audio.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("expected CoreAudio unavailable error, got %v", err)
	}
}
