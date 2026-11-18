//go:build !windows

package media

import (
	"sync"
	"time"

	"nexlab/internal/core"
)

type ScreenCapturer struct {
	mu      sync.Mutex
	width   int
	height  int
	seq     uint64
	backend CaptureBackend
	buf     []byte
}

func NewScreenCapturer(opts CaptureOptions) *ScreenCapturer {
	w := opts.Width
	if w <= 0 {
		w = 1280
	}
	h := opts.Height
	if h <= 0 {
		h = 720
	}
	return &ScreenCapturer{
		width:   w,
		height:  h,
		backend: CaptureBackendSynthetic,
	}
}

func (c *ScreenCapturer) Backend() CaptureBackend {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backend
}

func (c *ScreenCapturer) CaptureScreen() (*core.VideoFrame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.buf = renderSyntheticRGBA(c.buf, c.width, c.height, c.seq)
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	frame := &core.VideoFrame{
		Seq:       c.seq,
		Codec:     core.CodecFrameZstd,
		Width:     c.width,
		Height:    c.height,
		KeyFrame:  c.seq == 1 || c.seq%60 == 0,
		Timestamp: time.Now().UnixMilli(),
		Data:      out,
	}
	_ = EnsureNV12(frame)
	return frame, nil
}

func (c *ScreenCapturer) Close() error {
	return nil
}
