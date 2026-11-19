//go:build darwin

package media

import (
	"errors"
	"sync"

	"nexlab/internal/core"
)

var ErrDarwinCoreAudioUnavailable = errors.New("media: CoreAudio is unavailable; native Darwin bridge is not enabled")

type DarwinMediaCapabilityStatus struct {
	ScreenCaptureAvailable bool
	ScreenCaptureError     error
	CoreAudioAvailable     bool
	CoreAudioError         error
}

func DarwinMediaCapabilities() DarwinMediaCapabilityStatus {
	return DarwinMediaCapabilityStatus{
		ScreenCaptureError: ErrDarwinScreenCaptureUnavailable,
		CoreAudioError:     ErrDarwinCoreAudioUnavailable,
	}
}

type AudioEngine struct {
	mu     sync.Mutex
	volume float32
}

func NewAudioEngine() *AudioEngine { return &AudioEngine{volume: 1.0} }

func (a *AudioEngine) StartCapture(_ bool, _ func(*core.AudioFrame)) error {
	return ErrDarwinCoreAudioUnavailable
}

func (*AudioEngine) StopCapture() error { return nil }

func (*AudioEngine) PlayFrame(*core.AudioFrame) error {
	return ErrDarwinCoreAudioUnavailable
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

func (*AudioEngine) Close() error { return nil }
