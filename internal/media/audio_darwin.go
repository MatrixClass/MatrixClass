//go:build darwin

package media

import (
	"errors"
	"sync"

	"nexlab/internal/core"
)

var (
	ErrDarwinCoreAudioUnavailable   = errors.New("media: CoreAudio is unavailable; native Darwin bridge is not enabled")
	ErrDarwinCoreAudioPermission    = errors.New("media: macOS microphone permission is required")
	ErrDarwinAudioDeviceUnavailable = errors.New("media: macOS audio device is unavailable")
)

const (
	darwinCoreAudioFramework    = "/System/Library/Frameworks/CoreAudio.framework/CoreAudio"
	darwinAudioToolboxFramework = "/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox"
)

// DarwinAudioBridge is the boundary for a native CoreAudio/AudioToolbox
// implementation. The default bridge remains unavailable until the native
// callback and permission flow are implemented on macOS.
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
	return DarwinMediaCapabilityStatus{
		ScreenCaptureError:        ErrDarwinScreenCaptureUnavailable,
		CoreAudioError:            ErrDarwinCoreAudioUnavailable,
		MicrophonePermissionError: ErrDarwinCoreAudioPermission,
		AudioDeviceError:          ErrDarwinAudioDeviceUnavailable,
		CoreAudioFramework:        darwinCoreAudioFramework,
		AudioToolboxFramework:     darwinAudioToolboxFramework,
	}
}

type AudioEngine struct {
	mu     sync.Mutex
	volume float32
	bridge DarwinAudioBridge
}

func NewAudioEngine() *AudioEngine {
	return newDarwinAudioEngine(&darwinAudioUnavailableBridge{})
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
