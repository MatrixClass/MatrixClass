//go:build darwin

package media

import (
	"errors"
	"testing"

	"nexlab/internal/core"
)

// These tests run on real Macs (CI runners and developer machines) where
// Screen Recording / Microphone access may or may not be granted. They assert
// consistent behaviour for either outcome and never construct a native
// capturer without permission, because that would show a system prompt.

func TestDarwinScreenCaptureNeverFakesFrames(t *testing.T) {
	backend, probeErr := DarwinScreenCaptureProbe()
	if probeErr != nil {
		if !errors.Is(probeErr, ErrDarwinScreenCaptureUnavailable) {
			t.Fatalf("probe error must wrap ErrDarwinScreenCaptureUnavailable, got %v", probeErr)
		}
		if backend == CaptureBackendSynthetic {
			t.Fatalf("unavailable capture must not report a synthetic backend")
		}
		return
	}
	if !DarwinScreenCapturePermissionGranted() {
		t.Skip("screen recording permission not granted; skipping native capture to avoid a prompt")
	}
	capturer := NewScreenCapturer(CaptureOptions{Width: 320, Height: 180})
	defer capturer.Close()
	if got := capturer.Backend(); got != darwinScreenCaptureBackendSCK && got != darwinScreenCaptureBackendCG {
		t.Fatalf("expected a native macOS backend, got %q", got)
	}
	frame, err := capturer.CaptureScreen()
	if err != nil {
		t.Fatalf("native capture failed with permission granted: %v", err)
	}
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 || len(frame.Data) < frame.Width*frame.Height*4 {
		t.Fatalf("invalid native frame: %+v", frame)
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

func TestDarwinMediaCapabilitiesAreConsistent(t *testing.T) {
	status := DarwinMediaCapabilities()
	if status.ScreenCaptureAvailable != (status.ScreenCaptureError == nil) {
		t.Fatalf("screen availability must match its error: %+v", status)
	}
	if status.ScreenCaptureError != nil && !errors.Is(status.ScreenCaptureError, ErrDarwinScreenCaptureUnavailable) {
		t.Fatalf("unexpected screen capture error: %v", status.ScreenCaptureError)
	}
	if status.CoreAudioAvailable != (status.CoreAudioError == nil) {
		t.Fatalf("CoreAudio availability must match its error: %+v", status)
	}
	if status.MicrophonePermissionError != nil && !errors.Is(status.MicrophonePermissionError, ErrDarwinCoreAudioPermission) {
		t.Fatalf("unexpected microphone permission error: %v", status.MicrophonePermissionError)
	}
	if status.AudioDeviceError != nil && !errors.Is(status.AudioDeviceError, ErrDarwinAudioDeviceUnavailable) {
		t.Fatalf("unexpected audio device error: %v", status.AudioDeviceError)
	}
	if status.CoreAudioFramework == "" || status.AudioToolboxFramework == "" {
		t.Fatalf("expected native framework paths: %+v", status)
	}
}

func TestDarwinScreenCaptureStreamReportsLifecycleAndReconnectState(t *testing.T) {
	if _, err := DarwinScreenCaptureProbe(); err == nil {
		t.Skip("capture available on this Mac; lifecycle-unavailable path not reachable")
	}
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

// The fallback used when AudioToolbox/CoreAudio can't be loaded must report
// errors instead of generating silence.
func TestDarwinUnavailableAudioFallbackReportsErrors(t *testing.T) {
	audio := newDarwinAudioEngine(&darwinAudioUnavailableBridge{})
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

func TestDarwinAudioLoopbackIsExplicitlyUnsupported(t *testing.T) {
	status := DarwinMediaCapabilities()
	if !status.CoreAudioAvailable {
		t.Skip("CoreAudio not available on this machine")
	}
	audio := NewAudioEngine()
	defer audio.Close()
	if err := audio.StartCapture(true, func(*core.AudioFrame) {}); !errors.Is(err, ErrDarwinAudioLoopbackUnavailable) {
		t.Fatalf("expected loopback unavailable error, got %v", err)
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
