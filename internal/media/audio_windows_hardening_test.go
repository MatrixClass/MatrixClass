//go:build windows

package media

import "testing"

func TestConvertPCM16ToRenderRejectsHostileRatesAndClamps(t *testing.T) {
	pcm := make([]byte, 960*2*2)
	dst := wasapiFormat{SampleRate: 48000, Channels: 2, Bits: 32, IsFloat: true}
	if got := convertPCM16ToRender(pcm, 1, 2, dst); got != nil {
		t.Fatalf("inRate=1 produced %d bytes, want nil", len(got))
	}
	if got := convertPCM16ToRender(pcm, 10_000_000, 2, dst); got != nil {
		t.Fatalf("inRate=10M produced %d bytes, want nil", len(got))
	}
	// 2s of 8kHz mono upsampled to 192kHz must be truncated to one second of output.
	big := make([]byte, 16000*2)
	dstHi := wasapiFormat{SampleRate: 192000, Channels: 2, Bits: 16}
	got := convertPCM16ToRender(big, 8000, 1, dstHi)
	if want := 192000 * 2 * 2; len(got) != want {
		t.Fatalf("clamped output = %d bytes, want %d", len(got), want)
	}
	if got := convertPCM16ToRender(pcm, 44100, 2, dst); len(got) == 0 {
		t.Fatal("expected normal 44.1k->48k conversion to produce output")
	}
}
