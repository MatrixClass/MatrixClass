//go:build darwin

package media

import (
	"errors"
	"fmt"
	"sync"

	"nexlab/internal/core"
)

var (
	ErrDarwinCoreAudioUnavailable   = errors.New("media: CoreAudio is unavailable; native Darwin bridge is not enabled")
	ErrDarwinCoreAudioPermission    = errors.New("media: macOS microphone permission is required")
	ErrDarwinAudioDeviceUnavailable = errors.New("media: macOS audio device is unavailable")
	// ErrDarwinAudioLoopbackUnavailable is returned for loopback (system
	// audio) capture: macOS has no built-in loopback device, so it needs a
	// virtual audio driver or ScreenCaptureKit audio, neither of which the
	// AudioQueue bridge uses.
	ErrDarwinAudioLoopbackUnavailable = errors.New("media: macOS system audio (loopback) capture is unavailable without a virtual audio device")
	ErrDarwinAudioClosed              = errors.New("media: macOS audio engine is closed")
)

const (
	darwinCoreAudioFramework    = "/System/Library/Frameworks/CoreAudio.framework/CoreAudio"
	darwinAudioToolboxFramework = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"
)

// DarwinAudioBridge is the boundary for the native CoreAudio/AudioToolbox
// implementation. NewAudioEngine uses the AudioQueue bridge
// (audio_darwin_aq.go) and falls back to an unavailable bridge when the
// frameworks cannot be loaded.
type DarwinAudioBridge interface {
	StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error
	StopCapture() error
	PlayFrame(frame *core.AudioFrame, volume float32) error
	RefreshDevices() error
	Restart() error
	Close() error
}

type darwinAudioUnavailableBridge struct{}

func (darwinAudioUnavailableBridge) StartCapture(bool, func(*core.AudioFrame)) error {
	return ErrDarwinCoreAudioUnavailable
}

func (darwinAudioUnavailableBridge) StopCapture() error { return nil }

func (darwinAudioUnavailableBridge) PlayFrame(*core.AudioFrame, float32) error {
	return ErrDarwinCoreAudioUnavailable
}

func (darwinAudioUnavailableBridge) RefreshDevices() error {
	return ErrDarwinAudioDeviceUnavailable
}

func (darwinAudioUnavailableBridge) Restart() error {
	return ErrDarwinCoreAudioUnavailable
}

func (darwinAudioUnavailableBridge) Close() error { return nil }

type DarwinMediaCapabilityStatus struct {
	ScreenCaptureAvailable    bool
	ScreenCaptureError        error
	CoreAudioAvailable        bool
	CoreAudioError            error
	MicrophonePermissionError error
	AudioDeviceError          error
	CoreAudioFramework        string
	AudioToolboxFramework     string
}

func DarwinMediaCapabilities() DarwinMediaCapabilityStatus {
	// The probe never triggers a screen-recording permission prompt.
	_, screenErr := DarwinScreenCaptureProbe()
	status := DarwinMediaCapabilityStatus{
		ScreenCaptureAvailable: screenErr == nil,
		ScreenCaptureError:     screenErr,
		CoreAudioFramework:     darwinCoreAudioFramework,
		AudioToolboxFramework:  darwinAudioToolboxFramework,
	}
	native := newDarwinAudioAQNative()
	if err := native.Available(); err != nil {
		status.CoreAudioError = err
		status.MicrophonePermissionError = ErrDarwinCoreAudioPermission
		status.AudioDeviceError = ErrDarwinAudioDeviceUnavailable
		return status
	}
	status.CoreAudioAvailable = true
	status.MicrophonePermissionError = darwinAudioCapabilityPermission(native.MicrophoneAuthorization())
	if in, out, err := native.DefaultDevices(); err != nil {
		status.AudioDeviceError = fmt.Errorf("%w: %v", ErrDarwinAudioDeviceUnavailable, err)
	} else if in == aqAudioObjectUnknown && out == aqAudioObjectUnknown {
		status.AudioDeviceError = ErrDarwinAudioDeviceUnavailable
	} else if in == aqAudioObjectUnknown {
		status.AudioDeviceError = fmt.Errorf("%w: no default input device", ErrDarwinAudioDeviceUnavailable)
	} else if out == aqAudioObjectUnknown {
		status.AudioDeviceError = fmt.Errorf("%w: no default output device", ErrDarwinAudioDeviceUnavailable)
	}
	return status
}

// darwinAudioCapabilityPermission reports nil only when microphone access
// is granted; a pending prompt or unknown status is still reported as the
// permission error so callers do not assume capture will deliver audio.
func darwinAudioCapabilityPermission(status darwinAudioAuthStatus) error {
	if status == darwinAudioAuthAuthorized {
		return nil
	}
	return fmt.Errorf("%w (%s)", ErrDarwinCoreAudioPermission, status)
}

func darwinAudioErrors() darwinAudioBridgeErrors {
	return darwinAudioBridgeErrors{
		Unavailable: ErrDarwinCoreAudioUnavailable,
		Permission:  ErrDarwinCoreAudioPermission,
		Device:      ErrDarwinAudioDeviceUnavailable,
		Loopback:    ErrDarwinAudioLoopbackUnavailable,
		Closed:      ErrDarwinAudioClosed,
	}
}

// newDarwinDefaultAudioBridge returns the AudioQueue bridge, or the
// unavailable bridge when AudioToolbox/CoreAudio cannot be loaded.
func newDarwinDefaultAudioBridge() DarwinAudioBridge {
	native := newDarwinAudioAQNative()
	if native.Available() != nil {
		return &darwinAudioUnavailableBridge{}
	}
	return newDarwinAudioQueueBridge(native, darwinAudioErrors())
}

type AudioEngine struct {
	mu     sync.Mutex
	volume float32
	bridge DarwinAudioBridge
}

func NewAudioEngine() *AudioEngine {
	return newDarwinAudioEngine(newDarwinDefaultAudioBridge())
}

func newDarwinAudioEngine(bridge DarwinAudioBridge) *AudioEngine {
	if bridge == nil {
		bridge = &darwinAudioUnavailableBridge{}
	}
	return &AudioEngine{volume: 1.0, bridge: bridge}
}

func (a *AudioEngine) StartCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	a.mu.Lock()
	bridge := a.bridge
	a.mu.Unlock()
	return bridge.StartCapture(loopback, onFrame)
}

func (a *AudioEngine) StopCapture() error {
	a.mu.Lock()
	bridge := a.bridge
	a.mu.Unlock()
	return bridge.StopCapture()
}

func (a *AudioEngine) PlayFrame(frame *core.AudioFrame) error {
	a.mu.Lock()
	bridge := a.bridge
	volume := a.volume
	a.mu.Unlock()
	return bridge.PlayFrame(frame, volume)
}

// RefreshDevices asks the native bridge to re-enumerate input and output
// devices after a hot plug, default-device switch, or audio-service restart.
func (a *AudioEngine) RefreshDevices() error {
	a.mu.Lock()
	bridge := a.bridge
	a.mu.Unlock()
	return bridge.RefreshDevices()
}

// Restart stops the current native streams and lets the bridge recreate them
// against the current default devices. It never fabricates audio frames.
func (a *AudioEngine) Restart() error {
	a.mu.Lock()
	bridge := a.bridge
	a.mu.Unlock()
	return bridge.Restart()
}

func (a *AudioEngine) SetVolume(vol float32) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if vol < 0 {
		vol = 0
	} else if vol > 1 {
		vol = 1
	}
	a.volume = vol
}

func (a *AudioEngine) Volume() float32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.volume
}

func (a *AudioEngine) Close() error {
	a.mu.Lock()
	bridge := a.bridge
	a.mu.Unlock()
	return bridge.Close()
}
