//go:build !linux

package media

func newNativeScreenCapture(CaptureOptions) nativeScreenCapture { return nil }
