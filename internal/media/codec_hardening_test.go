package media

import (
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"nexlab/internal/core"
)

func zstdCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}

func TestZstdVideoDecodeRejectsOverflowingDimensions(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	// 2^31 x 2^31 makes w*h*4 wrap to 0 on 64-bit ints; keyframe would then
	// "succeed" with an empty reference and a following delta would write OOB.
	huge := 1 << 31
	payload := zstdCompress(t, []byte{})
	key := packVideoHeader(codecIDFrameZstd, videoFlagKeyFrame, 80, huge, huge, 1, 0, payload)
	if _, err := codec.Decode(key); !errors.Is(err, ErrInvalidDimension) {
		t.Fatalf("overflowing keyframe err = %v, want ErrInvalidDimension", err)
	}

	delta := make([]byte, 4+8+4)
	binary.LittleEndian.PutUint32(delta[0:4], 1)
	binary.LittleEndian.PutUint16(delta[4:6], 100)
	binary.LittleEndian.PutUint16(delta[6:8], 100)
	binary.LittleEndian.PutUint16(delta[8:10], 1)
	binary.LittleEndian.PutUint16(delta[10:12], 1)
	deltaFrame := packVideoHeader(codecIDFrameZstd, videoFlagDelta, 80, huge, huge, 2, 0, zstdCompress(t, delta))
	if _, err := codec.Decode(deltaFrame); err == nil {
		t.Fatal("expected overflowing delta frame to be rejected")
	}

	for _, dim := range [][2]int{{MaxVideoDimension + 1, 16}, {16, MaxVideoDimension + 1}, {MaxVideoDimension, MaxVideoDimension}} {
		frame := packVideoHeader(codecIDFrameZstd, videoFlagKeyFrame, 80, dim[0], dim[1], 1, 0, payload)
		if _, err := codec.Decode(frame); !errors.Is(err, ErrInvalidDimension) {
			t.Fatalf("Decode(%dx%d) err = %v, want ErrInvalidDimension", dim[0], dim[1], err)
		}
	}
	if !ValidVideoDimensions(7680, 4320) || !ValidVideoDimensions(1, 1) {
		t.Fatal("expected 8K and 1x1 frames to be valid")
	}
}

func TestZstdVideoDecodeDeltaWithoutReferenceFails(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	delta := make([]byte, 4)
	frame := packVideoHeader(codecIDFrameZstd, videoFlagDelta, 80, 64, 64, 1, 0, zstdCompress(t, delta))
	if _, err := codec.Decode(frame); !errors.Is(err, ErrInvalidFrameData) {
		t.Fatalf("delta without reference err = %v, want ErrInvalidFrameData", err)
	}
}

func TestZstdVideoDecodeRejectsOversizedDecompression(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()
	// Highly compressible bomb larger than any legal frame.
	bomb := zstdCompress(t, make([]byte, maxVideoRawBytes+1024))
	frame := packVideoHeader(codecIDFrameZstd, videoFlagKeyFrame, 80, 64, 64, 1, 0, bomb)
	if _, err := codec.Decode(frame); err == nil {
		t.Fatal("expected decompression bomb to be rejected")
	}
}

func TestZstdVideoCodecCloseConcurrentWithDecode(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatal(err)
	}
	w, h := 64, 32
	src := &core.VideoFrame{Seq: 1, Width: w, Height: h, KeyFrame: true, Data: make([]byte, w*h*4)}
	encoded, err := codec.Encode(src, 90)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = codec.Decode(encoded)
			}
		}()
	}
	_ = codec.Close()
	wg.Wait()
	if _, err := codec.Decode(encoded); !errors.Is(err, ErrCodecClosed) {
		t.Fatalf("Decode after Close err = %v, want ErrCodecClosed", err)
	}
	if _, err := codec.Encode(src, 90); !errors.Is(err, ErrCodecClosed) {
		t.Fatalf("Encode after Close err = %v, want ErrCodecClosed", err)
	}
	if err := codec.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestHybridVideoCodecConcurrentMFTDecodeAndClose(t *testing.T) {
	hybrid, err := NewHybridVideoCodec(false)
	if err != nil {
		t.Fatal(err)
	}
	frame := packVideoHeader(codecIDH264MFT, videoFlagKeyFrame, 80, 64, 64, 1, 0, []byte{0, 0, 0, 1})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = hybrid.Decode(frame)
			_ = hybrid.MFTAvailable()
		}()
	}
	wg.Wait()
	_ = hybrid.Close()
}

func encodeAudioHeader(channels int, flags uint8, sampleRate, sampleCount int, payload []byte) []byte {
	out := make([]byte, audioHdrSize+len(payload))
	copy(out[0:4], audioMagic)
	out[4] = audioVersion
	out[5] = uint8(channels)
	out[6] = flags
	binary.LittleEndian.PutUint32(out[8:12], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:32], uint32(sampleCount))
	binary.LittleEndian.PutUint32(out[32:36], uint32(len(payload)))
	copy(out[audioHdrSize:], payload)
	return out
}

func TestAudioCodecDecodeRejectsHostileHeaders(t *testing.T) {
	codec, err := NewAudioCodec()
	if err != nil {
		t.Fatal(err)
	}
	defer codec.Close()

	cases := []struct {
		name        string
		channels    int
		sampleRate  int
		sampleCount int
	}{
		{"huge sample count", 2, 48000, 1<<32 - 1},
		{"sample count above one second", 2, 48000, 48001},
		{"sample rate too low", 2, 1, 10},
		{"sample rate too high", 2, 10_000_000, 10},
		{"too many channels", 9, 48000, 10},
	}
	for _, tc := range cases {
		payload := make([]byte, 64)
		data := encodeAudioHeader(tc.channels, 0, tc.sampleRate, tc.sampleCount, payload)
		if _, err := codec.Decode(data); !errors.Is(err, ErrInvalidAudioData) {
			t.Fatalf("%s: err = %v, want ErrInvalidAudioData", tc.name, err)
		}
	}

	bomb := zstdCompress(t, make([]byte, maxAudioADPCMBytes+4096))
	data := encodeAudioHeader(2, audioFlagZstd, 48000, 960, bomb)
	if _, err := codec.Decode(data); err == nil {
		t.Fatal("expected audio decompression bomb to be rejected")
	}
}

func TestAudioCodecRoundTripTypicalFrameAndClose(t *testing.T) {
	codec, err := NewAudioCodec()
	if err != nil {
		t.Fatal(err)
	}
	frame := &core.AudioFrame{Seq: 1, SampleRate: 48000, Channels: 2, Data: make([]byte, 4800*2*2)}
	enc, err := codec.Encode(frame)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := codec.Decode(enc)
	if err != nil {
		t.Fatalf("Decode 100ms frame: %v", err)
	}
	if len(dec.Data) != len(frame.Data) {
		t.Fatalf("decoded %d bytes, want %d", len(dec.Data), len(frame.Data))
	}
	_ = codec.Close()
	if _, err := codec.Decode(enc); !errors.Is(err, ErrCodecClosed) {
		t.Fatalf("Decode after Close err = %v, want ErrCodecClosed", err)
	}
	if _, err := codec.Encode(frame); !errors.Is(err, ErrCodecClosed) {
		t.Fatalf("Encode after Close err = %v, want ErrCodecClosed", err)
	}
}
