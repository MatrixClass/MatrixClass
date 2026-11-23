//go:build !windows && !linux && !darwin

package media

import (
	"errors"
	"sync"

	"nexlab/internal/core"
)

var ErrAudioUnsupportedPlatform = errors.New("media: audio is unsupported on this platform")

type AudioEngine struct {
	mu     sync.Mutex
	volume float32
	stopCh chan struct{}
	doneCh chan struct{}
	seq    uint64
}

func NewAudioEngine() *AudioEngine {
	return &AudioEngine{volume: 1.0}
}

func (a *AudioEngine) StartCapture(_ bool, onFrame func(*core.AudioFrame)) error {
	return ErrAudioUnsupportedPlatform
}

func (a *AudioEngine) StopCapture() error {
	a.mu.Lock()
	stopCh := a.stopCh
	doneCh := a.doneCh
	a.stopCh = nil
	a.doneCh = nil
	a.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if doneCh != nil {
		<-doneCh
	}
	return nil
}

func (a *AudioEngine) PlayFrame(_ *core.AudioFrame) error {
	return ErrAudioUnsupportedPlatform
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
	return a.StopCapture()
}
