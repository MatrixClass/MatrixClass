//go:build windows

package media

import (
	"testing"
	"unsafe"
)

func TestWindowsNativeABIAndBufferGuards(t *testing.T) {
	if unsafe.Offsetof(waveFormatEx{}.NSamplesPerSec) != 4 || unsafe.Offsetof(waveFormatEx{}.CbSize) != 16 {
		t.Fatalf("waveFormatEx field offsets do not match Windows ABI: size=%d", unsafe.Sizeof(waveFormatEx{}))
	}
	if unsafe.Sizeof(waveFormatEx{}) != 20 {
		t.Fatalf("waveFormatEx aligned size = %d, want 20", unsafe.Sizeof(waveFormatEx{}))
	}
	if unsafe.Sizeof(d3d11MappedSubresource{}) != unsafe.Sizeof(uintptr(0))+8 {
		t.Fatalf("d3d11MappedSubresource size = %d", unsafe.Sizeof(d3d11MappedSubresource{}))
	}
	if got, ok := wasapiFrameBytes(480, wasapiFormat{Channels: 2, Bits: 16}); !ok || got != 1920 {
		t.Fatalf("valid WASAPI frame size = %d, %v", got, ok)
	}
	if _, ok := wasapiFrameBytes(1, wasapiFormat{Channels: int(^uint(0) >> 1), Bits: 64}); ok {
		t.Fatal("expected oversized WASAPI frame to be rejected")
	}
	if validWindowsFrameDimensions(0, 1) || validWindowsFrameDimensions(1, 0) {
		t.Fatal("expected zero dimensions to be rejected")
	}
	if !validWindowsFrameDimensions(1920, 1080) {
		t.Fatal("expected normal dimensions to be accepted")
	}
}

func TestWindowsNativeHelpersRejectNilOrInvalidFormats(t *testing.T) {
	got := parseWaveFormat(0)
	if got.SampleRate != 48000 || got.Channels != 2 || got.Bits != 16 {
		t.Fatalf("nil wave format fallback = %+v", got)
	}
	if got := convertPCM16ToRender([]byte{0, 0}, 48000, 1, wasapiFormat{Channels: 1, Bits: 24}); got != nil {
		t.Fatal("expected unsupported render bit depth to return nil")
	}
	if comMethod(0, 0) != 0 || comCall(0, 0) != 0 {
		t.Fatal("nil COM object must not be dereferenced")
	}
}
