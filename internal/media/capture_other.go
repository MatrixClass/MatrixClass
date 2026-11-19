//go:build !windows

package media

import (
	"errors"
	"sync"
	"time"

	"nexlab/internal/core"
)

var ErrScreenCaptureUnavailable = errors.New("media: native screen capture is unavailable")

type ScreenCapturer struct {
	mu      sync.Mutex
	width   int
	height  int
	seq     uint64
	backend CaptureBackend
	buf     []byte
	changed bool
	native  nativeScreenCapture
	err     error
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
	c := &ScreenCapturer{
		width:   w,
		height:  h,
		backend: CaptureBackendSynthetic,
	}
	if !opts.ForceSynthetic {
		c.native = newNativeScreenCapture(opts)
		if c.native != nil {
			c.backend = c.native.Backend()
		} else {
			c.backend = CaptureBackendUnavailable
		}
	}
	return c
}

func (c *ScreenCapturer) Backend() CaptureBackend {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backend
}

func (c *ScreenCapturer) CaptureScreen() (*core.VideoFrame, error) {
	return c.CaptureScreenScaled(0, 0, true)
}

func (c *ScreenCapturer) CaptureScreenScaled(maxW, maxH int, withNV12 bool) (*core.VideoFrame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native != nil {
		frame, changed, err := c.native.Capture(maxW, maxH, withNV12)
		if err == nil && frame != nil {
			c.changed = changed
			c.err = nil
			return frame, nil
		}
		c.err = err
		if c.backend != CaptureBackendSynthetic {
			if err == nil {
				err = ErrScreenCaptureUnavailable
			}
			return nil, err
		}
		_ = c.native.Close()
		c.native = nil
		c.backend = CaptureBackendSynthetic
	}
	if c.backend == CaptureBackendUnavailable {
		if c.err == nil {
			c.err = ErrScreenCaptureUnavailable
		}
		return nil, c.err
	}
	c.seq++
	c.changed = true
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
	if (maxW > 0 && frame.Width > maxW) || (maxH > 0 && frame.Height > maxH) {
		frame = DownscaleFrame(frame, maxW, maxH)
	}
	if withNV12 {
		_ = EnsureNV12(frame)
	}
	return frame, nil
}

func (c *ScreenCapturer) LastFrameChanged() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := c.changed
	c.changed = false
	return changed
}

func (c *ScreenCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.native != nil {
		err := c.native.Close()
		c.native = nil
		return err
	}
	return nil
}
