//go:build !linux && !darwin

package media

func newNativeScreenCapture(CaptureOptions) nativeScreenCapture { return nil }
