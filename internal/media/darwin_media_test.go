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

func TestDarwinScreenCaptureStreamReportsLifecycleAndReconnectState(t *testing.T) {
	stream := NewDarwinScreenCaptureStream(CaptureOptions{Width: 320, Height: 180})
	if stream == nil {
		t.Fatal("expected Darwin screen capture stream")
	}
	if got := stream.State(); got != DarwinScreenCaptureStateUnavailable {
		t.Fatalf("initial state = %q, want %q", got, DarwinScreenCaptureStateUnavailable)
	}
	if err := stream.Start(); !errors.Is(err, ErrDarwinScreenCaptureUnavailable) {
		t.Fatalf("Start error = %v, want unavailable error", err)
	}
	if got := stream.State(); got != DarwinScreenCaptureStateUnavailable {
		t.Fatalf("state after failed start = %q, want %q", got, DarwinScreenCaptureStateUnavailable)
	}
	if err := stream.Reconnect(); !errors.Is(err, ErrDarwinScreenCaptureUnavailable) {
		t.Fatalf("Reconnect error = %v, want unavailable error", err)
	}
	if got := stream.ReconnectAttempts(); got != 1 {
		t.Fatalf("reconnect attempts = %d, want 1", got)
	}
	if err := stream.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestDarwinScreenCaptureStreamCloseIsIdempotent(t *testing.T) {
	stream := NewDarwinScreenCaptureStream(CaptureOptions{})
	if err := stream.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

func TestDarwinAudioReportsUnavailableInsteadOfGeneratingSilence(t *testing.T) {
	audio := NewAudioEngine()
	defer audio.Close()

	if err := audio.StartCapture(false, func(*core.AudioFrame) {}); !errors.Is(err, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("expected CoreAudio unavailable error, got %v", err)
	}
	if err := audio.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{0, 0}}); !errors.Is(err, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("expected unavailable playback error, got %v", err)
	}
	if err := audio.RefreshDevices(); !errors.Is(err, ErrDarwinAudioDeviceUnavailable) {
		t.Fatalf("expected unavailable device error, got %v", err)
	}
	if err := audio.Restart(); !errors.Is(err, ErrDarwinCoreAudioUnavailable) {
		t.Fatalf("expected unavailable restart error, got %v", err)
	}
}

type recordingDarwinAudioBridge struct {
	startLoopback bool
	startCallback func(*core.AudioFrame)
	playVolume    float32
	refreshed     bool
	restarted     bool
	closed        bool
}

func (b *recordingDarwinAudioBridge) StartCapture(loopback bool, callback func(*core.AudioFrame)) error {
	b.startLoopback = loopback
	b.startCallback = callback
	return nil
}

func (*recordingDarwinAudioBridge) StopCapture() error { return nil }

func (b *recordingDarwinAudioBridge) PlayFrame(_ *core.AudioFrame, volume float32) error {
	b.playVolume = volume
	return nil
}

func (b *recordingDarwinAudioBridge) RefreshDevices() error {
	b.refreshed = true
	return nil
}

func (b *recordingDarwinAudioBridge) Restart() error {
	b.restarted = true
	return nil
}

func (b *recordingDarwinAudioBridge) Close() error {
	b.closed = true
	return nil
}

func TestDarwinAudioBridgeReceivesLifecycleAndVolumeCalls(t *testing.T) {
	bridge := &recordingDarwinAudioBridge{}
	audio := newDarwinAudioEngine(bridge)
	audio.SetVolume(0.35)
	callback := func(*core.AudioFrame) {}

	if err := audio.StartCapture(true, callback); err != nil {
		t.Fatalf("StartCapture failed: %v", err)
	}
	if err := audio.PlayFrame(&core.AudioFrame{SampleRate: 48000, Channels: 2, Data: []byte{0, 0}}); err != nil {
		t.Fatalf("PlayFrame failed: %v", err)
	}
	if err := audio.RefreshDevices(); err != nil {
		t.Fatalf("RefreshDevices failed: %v", err)
	}
	if err := audio.Restart(); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	if err := audio.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !bridge.startLoopback || bridge.startCallback == nil || bridge.playVolume != 0.35 || !bridge.refreshed || !bridge.restarted || !bridge.closed {
		t.Fatalf("unexpected bridge calls: %+v", bridge)
	}
}

func TestDarwinMediaCapabilitiesExposeAudioBridgeBoundaries(t *testing.T) {
	status := DarwinMediaCapabilities()
	if !errors.Is(status.MicrophonePermissionError, ErrDarwinCoreAudioPermission) {
		t.Fatalf("unexpected microphone permission error: %v", status.MicrophonePermissionError)
	}
	if !errors.Is(status.AudioDeviceError, ErrDarwinAudioDeviceUnavailable) {
		t.Fatalf("unexpected audio device error: %v", status.AudioDeviceError)
	}
	if status.CoreAudioFramework == "" || status.AudioToolboxFramework == "" {
		t.Fatalf("expected native framework paths: %+v", status)
	}
}
