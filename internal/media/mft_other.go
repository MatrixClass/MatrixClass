//go:build !windows

package media

import "nexlab/internal/core"

type noopVideoMFT struct{}

func newPlatformVideoMFT() videoMFTBackend {
	return &noopVideoMFT{}
}

func (n *noopVideoMFT) Available() bool { return false }

func (n *noopVideoMFT) Encode(_ *core.VideoFrame, _ int) ([]byte, error) {
	return nil, ErrMFTUnavailable
}

func (n *noopVideoMFT) Decode(_ []byte) (*core.VideoFrame, error) {
	return nil, ErrMFTUnavailable
}

func (n *noopVideoMFT) Close() error { return nil }
