//go:build darwin

package media

import (
	"errors"
	"fmt"

	"nexlab/internal/core"
)

var (
	ErrDarwinScreenCaptureUnavailable = errors.New("media: ScreenCaptureKit capture is unavailable; native Darwin bridge is not enabled")
	ErrDarwinScreenCapturePermission  = errors.New("media: ScreenCaptureKit permission is required")
)

type darwinScreenCaptureUnavailable struct{}

func (darwinScreenCaptureUnavailable) Backend() CaptureBackend { return CaptureBackendUnavailable }

func (darwinScreenCaptureUnavailable) Capture(int, int, bool) (*core.VideoFrame, bool, error) {
	return nil, false, ErrDarwinScreenCaptureUnavailable
}

func (darwinScreenCaptureUnavailable) Close() error { return nil }

func newNativeScreenCapture(CaptureOptions) nativeScreenCapture {
	return darwinScreenCaptureUnavailable{}
}

func (darwinScreenCaptureUnavailable) String() string {
	return fmt.Sprintf("%s", ErrDarwinScreenCaptureUnavailable)
}
