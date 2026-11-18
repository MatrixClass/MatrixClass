package media

import (
	"errors"
	"sync"

	"nexlab/internal/core"
)

var _ core.MediaEngine = (*Manager)(nil)

type ManagerOptions struct {
	Capture       CaptureOptions
	PreferH264MFT bool
}

// Manager implements core.MediaEngine, combining screen capture,
// video/audio encoding & decoding, WASAPI audio capture/playback, and .nxlr recording.
type Manager struct {
	capturer      *ScreenCapturer
	videoCodec    *HybridVideoCodec
	audioCodec    *AudioCodec
	audio         *AudioEngine
	preferH264MFT bool

	recMu    sync.Mutex
	recorder *LessonRecorder
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	vc, err := NewHybridVideoCodec(opts.PreferH264MFT)
	if err != nil {
		return nil, err
	}
	ac, err := NewAudioCodec()
	if err != nil {
		_ = vc.Close()
		return nil, err
	}
	return &Manager{
		capturer:      NewScreenCapturer(opts.Capture),
		videoCodec:    vc,
		audioCodec:    ac,
		audio:         NewAudioEngine(),
		preferH264MFT: opts.PreferH264MFT,
	}, nil
}

func (m *Manager) CaptureScreen() (*core.VideoFrame, error) {
	return m.capturer.CaptureScreen()
}

func (m *Manager) CaptureScreenMax(maxW, maxH int) (*core.VideoFrame, error) {
	return m.capturer.CaptureScreenScaled(maxW, maxH, m.preferH264MFT)
}

func (m *Manager) CaptureBackend() CaptureBackend {
	return m.capturer.Backend()
}

func (m *Manager) LastFrameChanged() bool {
	return m.capturer.LastFrameChanged()
}

func (m *Manager) MFTAvailable() bool {
	return m.videoCodec.MFTAvailable()
}

func (m *Manager) EncodeFrame(frame *core.VideoFrame, quality int) ([]byte, error) {
	encoded, err := m.videoCodec.Encode(frame, quality)
	if err != nil {
		return nil, err
	}
	m.recMu.Lock()
	rec := m.recorder
	m.recMu.Unlock()
	if rec != nil && frame != nil {
		_ = rec.WriteVideo(frame.Seq, frame.Timestamp, frame.KeyFrame, encoded)
	}
	return encoded, nil
}

func (m *Manager) DecodeFrame(data []byte) (*core.VideoFrame, error) {
	return m.videoCodec.Decode(data)
}

func (m *Manager) EncodeAudioFrame(frame *core.AudioFrame) ([]byte, error) {
	encoded, err := m.audioCodec.Encode(frame)
	if err != nil {
		return nil, err
	}
	m.recMu.Lock()
	rec := m.recorder
	m.recMu.Unlock()
	if rec != nil && frame != nil {
		_ = rec.WriteAudio(frame.Seq, frame.Timestamp, encoded)
	}
	return encoded, nil
}

func (m *Manager) DecodeAudioFrame(data []byte) (*core.AudioFrame, error) {
	return m.audioCodec.Decode(data)
}

func (m *Manager) StartAudioCapture(loopback bool, onFrame func(*core.AudioFrame)) error {
	return m.audio.StartCapture(loopback, onFrame)
}

func (m *Manager) StopAudioCapture() error {
	return m.audio.StopCapture()
}

func (m *Manager) PlayAudioFrame(frame *core.AudioFrame) error {
	return m.audio.PlayFrame(frame)
}

func (m *Manager) SetVolume(vol float32) {
	m.audio.SetVolume(vol)
}

func (m *Manager) Volume() float32 {
	return m.audio.Volume()
}

func (m *Manager) StartRecording(path string, width, height int) error {
	rec, err := NewLessonRecorder(path, width, height)
	if err != nil {
		return err
	}
	m.recMu.Lock()
	prev := m.recorder
	m.recorder = rec
	m.recMu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	return nil
}

func (m *Manager) StopRecording() error {
	m.recMu.Lock()
	rec := m.recorder
	m.recorder = nil
	m.recMu.Unlock()
	if rec == nil {
		return nil
	}
	return rec.Close()
}

func (m *Manager) RecordingStats() (RecorderStats, bool) {
	m.recMu.Lock()
	rec := m.recorder
	m.recMu.Unlock()
	if rec == nil {
		return RecorderStats{}, false
	}
	return rec.Stats(), true
}

func (m *Manager) Close() error {
	return errors.Join(
		m.StopRecording(),
		m.audio.Close(),
		m.capturer.Close(),
		m.videoCodec.Close(),
		m.audioCodec.Close(),
	)
}
