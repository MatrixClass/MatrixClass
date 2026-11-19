//go:build !windows

package media

import "testing"

func TestSyntheticCaptureReportsFrameChanges(t *testing.T) {
	capturer := NewScreenCapturer(CaptureOptions{Width: 16, Height: 16, ForceSynthetic: true})
	if _, err := capturer.CaptureScreen(); err != nil {
		t.Fatalf("CaptureScreen failed: %v", err)
	}
	if !capturer.LastFrameChanged() {
		t.Fatal("expected first synthetic frame to be marked changed")
	}
	if capturer.LastFrameChanged() {
		t.Fatal("expected change flag to be consumed")
	}
}
