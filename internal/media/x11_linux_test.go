//go:build linux

package media

import "testing"

func TestX11RGBAChangeDetection(t *testing.T) {
	first := []byte{1, 2, 3, 4, 5, 6}
	if !x11FrameChanged(first, 0, false) {
		t.Fatal("expected first frame to be changed")
	}
	if x11FrameChanged(first, x11FrameHash(first), true) {
		t.Fatal("expected identical frame to be unchanged")
	}
	second := append([]byte(nil), first...)
	second[len(second)-1]++
	if !x11FrameChanged(second, x11FrameHash(first), true) {
		t.Fatal("expected modified frame to be changed")
	}
}

func TestX11ReadPixelSupports24BPP(t *testing.T) {
	pixel := x11ReadPixel([]byte{0x11, 0x22, 0x33}, 24, 0)
	if pixel != 0x00332211 {
		t.Fatalf("unexpected little-endian 24bpp pixel: %#x", pixel)
	}
	pixel = x11ReadPixel([]byte{0x11, 0x22, 0x33}, 24, 1)
	if pixel != 0x00112233 {
		t.Fatalf("unexpected big-endian 24bpp pixel: %#x", pixel)
	}
}

func TestX11CaptureReconnectsWhenDisplayHandleIsLost(t *testing.T) {
	opened := 0
	closed := 0
	capture := &x11Capture{
		openDisplay: func(*byte) uintptr {
			opened++
			return 22
		},
		defaultRoot: func(uintptr) uintptr { return 33 },
		closeDisplay: func(uintptr) int32 {
			closed++
			return 1
		},
		getGeometry: func(_ uintptr, _ uintptr, _ *uintptr, _ *int32, _ *int32, width, height, _, _ *uint32) int32 {
			*width, *height = 1920, 1080
			return 1
		},
		maxWidth:  640,
		maxHeight: 360,
	}
	if !capture.reopen() {
		t.Fatal("expected display reconnect")
	}
	if opened != 1 || closed != 0 || capture.display != 22 || capture.root != 33 || capture.width != 640 || capture.height != 360 {
		t.Fatalf("unexpected reconnect state: opened=%d closed=%d capture=%+v", opened, closed, capture)
	}
}
