//go:build !windows && !linux && !darwin

package media

import (
	"sync"
	"time"

	"nexlab/internal/core"
)

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
	if onFrame == nil {
		return nil
	}
	_ = a.StopCapture()

	a.mu.Lock()
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	a.stopCh = stopCh
	a.doneCh = doneCh
	a.mu.Unlock()

	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		pcm := make([]byte, 960*2*2)
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				a.mu.Lock()
				a.seq++
				seq := a.seq
				a.mu.Unlock()
				cp := make([]byte, len(pcm))
				copy(cp, pcm)
				onFrame(&core.AudioFrame{
					Seq:        seq,
					SampleRate: 48000,
					Channels:   2,
					Timestamp:  time.Now().UnixMilli(),
					Data:       cp,
				})
			}
		}
	}()
	return nil
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
	return nil
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
