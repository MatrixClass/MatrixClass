package media

import "nexlab/internal/core"

type nativeScreenCapture interface {
	Backend() CaptureBackend
	Capture(maxW, maxH int, withNV12 bool) (*core.VideoFrame, bool, error)
	Close() error
}
