package media

import (
	"bytes"
	"encoding/binary"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nexlab/internal/core"
)

func TestRGBANV12RoundTrip(t *testing.T) {
	for _, dims := range [][2]int{{128, 72}, {127, 71}} {
		w, h := dims[0], dims[1]
		rgba := make([]byte, w*h*4)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				off := (y*w + x) * 4
				// Smooth gradient so 4:2:0 chroma subsampling stays accurate.
				rgba[off] = uint8((x * 255) / maxInt(1, w-1))
				rgba[off+1] = uint8((y * 255) / maxInt(1, h-1))
				rgba[off+2] = uint8(((x + y) * 255) / maxInt(1, w+h-2))
				rgba[off+3] = 255
			}
		}

		yPlane, uvPlane, sy, suv, err := RGBAToNV12(rgba, w, h)
		if err != nil {
			t.Fatalf("RGBAToNV12(%dx%d): %v", w, h, err)
		}
		out, err := NV12ToRGBA(yPlane, uvPlane, w, h, sy, suv)
		if err != nil {
			t.Fatalf("NV12ToRGBA(%dx%d): %v", w, h, err)
		}
		if len(out) != len(rgba) {
			t.Fatalf("unexpected output length %d != %d", len(out), len(rgba))
		}

		var maxDiff int
		for i := 0; i < len(rgba); i += 4 {
			for c := 0; c < 3; c++ {
				d := int(rgba[i+c]) - int(out[i+c])
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff = d
				}
			}
			if out[i+3] != 255 {
				t.Fatalf("expected opaque alpha 255, got %d", out[i+3])
			}
		}
		if maxDiff > 12 {
			t.Fatalf("RGBAToNV12->NV12ToRGBA(%dx%d) max channel diff %d too high", w, h, maxDiff)
		}
	}
}

func TestCodecFrameZstdKeyframeAndDeltaRoundTrip(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatalf("NewZstdVideoCodec: %v", err)
	}
	defer codec.Close()

	const w, h = 160, 96
	rgba1 := renderSyntheticRGBA(nil, w, h, 1)
	f1 := &core.VideoFrame{
		Seq:       1,
		Codec:     core.CodecFrameZstd,
		Width:     w,
		Height:    h,
		KeyFrame:  true,
		Timestamp: 1000,
		Data:      rgba1,
	}

	enc1, err := codec.Encode(f1, 85)
	if err != nil {
		t.Fatalf("Encode keyframe: %v", err)
	}
	dec1, err := codec.Decode(enc1)
	if err != nil {
		t.Fatalf("Decode keyframe: %v", err)
	}
	if !bytes.Equal(dec1.Data, rgba1) {
		t.Fatalf("keyframe RGBA mismatch")
	}
	if dec1.Seq != 1 || !dec1.KeyFrame || dec1.Width != w || dec1.Height != h {
		t.Fatalf("unexpected decoded keyframe metadata: %+v", dec1)
	}

	// Modify a small 16x16 region and encode a delta frame.
	rgba2 := make([]byte, len(rgba1))
	copy(rgba2, rgba1)
	for y := 10; y < 26; y++ {
		for x := 10; x < 26; x++ {
			off := (y*w + x) * 4
			rgba2[off] = 240
			rgba2[off+1] = 40
			rgba2[off+2] = 40
			rgba2[off+3] = 255
		}
	}
	f2 := &core.VideoFrame{
		Seq:       2,
		Codec:     core.CodecFrameZstd,
		Width:     w,
		Height:    h,
		KeyFrame:  false,
		Timestamp: 1033,
		Data:      rgba2,
	}
	enc2, err := codec.Encode(f2, 85)
	if err != nil {
		t.Fatalf("Encode delta: %v", err)
	}
	if len(enc2) >= len(enc1) {
		t.Fatalf("expected delta packet (%d bytes) to be smaller than keyframe (%d bytes)", len(enc2), len(enc1))
	}
	dec2, err := codec.Decode(enc2)
	if err != nil {
		t.Fatalf("Decode delta: %v", err)
	}
	if !bytes.Equal(dec2.Data, rgba2) {
		t.Fatalf("delta RGBA mismatch")
	}
	if dec2.Seq != 2 || dec2.KeyFrame || dec2.Timestamp != 1033 {
		t.Fatalf("unexpected decoded delta metadata: %+v", dec2)
	}
}

func TestAudioCodecRoundTrip(t *testing.T) {
	codec, err := NewAudioCodec()
	if err != nil {
		t.Fatalf("NewAudioCodec: %v", err)
	}
	defer codec.Close()

	const (
		sampleRate  = 48000
		channels    = 2
		sampleCount = 960
	)
	pcm := make([]byte, sampleCount*channels*2)
	for i := 0; i < sampleCount; i++ {
		left := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
		right := int16(10000 * math.Cos(2*math.Pi*660*float64(i)/sampleRate))
		binary.LittleEndian.PutUint16(pcm[(i*2)*2:(i*2)*2+2], uint16(left))
		binary.LittleEndian.PutUint16(pcm[(i*2+1)*2:(i*2+1)*2+2], uint16(right))
	}

	in := &core.AudioFrame{
		Seq:        42,
		SampleRate: sampleRate,
		Channels:   channels,
		Timestamp:  123456,
		Data:       pcm,
	}
	encoded, err := codec.Encode(in)
	if err != nil {
		t.Fatalf("AudioCodec.Encode: %v", err)
	}
	if len(encoded) >= len(pcm) {
		t.Fatalf("expected compressed audio (%d bytes) < raw PCM (%d bytes)", len(encoded), len(pcm))
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("AudioCodec.Decode: %v", err)
	}
	if decoded.Seq != in.Seq || decoded.SampleRate != in.SampleRate || decoded.Channels != in.Channels || decoded.Timestamp != in.Timestamp {
		t.Fatalf("audio metadata mismatch: got %+v want %+v", decoded, in)
	}
	if len(decoded.Data) != len(pcm) {
		t.Fatalf("decoded PCM size mismatch: got %d want %d", len(decoded.Data), len(pcm))
	}

	var sumSqErr float64
	var sumSqSig float64
	for i := 0; i < len(pcm); i += 2 {
		s0 := float64(int16(binary.LittleEndian.Uint16(pcm[i : i+2])))
		s1 := float64(int16(binary.LittleEndian.Uint16(decoded.Data[i : i+2])))
		diff := s0 - s1
		sumSqErr += diff * diff
		sumSqSig += s0 * s0
	}
	snr := 10 * math.Log10(sumSqSig/sumSqErr)
	if snr < 25.0 {
		t.Fatalf("unexpectedly low ADPCM SNR: %.2f dB", snr)
	}
}

func TestLessonRecorderRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lesson_01.nxlr")
	rec, err := NewLessonRecorder(path, 1280, 720)
	if err != nil {
		t.Fatalf("NewLessonRecorder: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				seq := uint64(worker*10 + i + 1)
				if err := rec.WriteVideo(seq, int64(seq*33), i == 0, []byte{1, 2, 3, byte(seq)}); err != nil {
					t.Errorf("WriteVideo: %v", err)
				}
				if err := rec.WriteAudio(seq, int64(seq*33), []byte{9, 8, byte(seq)}); err != nil {
					t.Errorf("WriteAudio: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	if err := rec.WriteMarker(500, "chapter-1"); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	stats := rec.Stats()
	if stats.VideoFrames != 40 || stats.AudioFrames != 40 {
		t.Fatalf("unexpected recorder stats: %+v", stats)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("rec.Close: %v", err)
	}

	reader, err := OpenLessonReader(path)
	if err != nil {
		t.Fatalf("OpenLessonReader: %v", err)
	}
	defer reader.Close()

	hdr := reader.Header()
	if hdr.Width != 1280 || hdr.Height != 720 || hdr.Version != nxlrVersion {
		t.Fatalf("unexpected lesson header: %+v", hdr)
	}
	chunks, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(chunks) != 81 {
		t.Fatalf("expected 81 chunks, got %d", len(chunks))
	}
	last := chunks[len(chunks)-1]
	if last.Kind != RecordMarker || string(last.Payload) != "chapter-1" {
		t.Fatalf("unexpected last chunk: %+v", last)
	}
}

func TestCaptureScreenAndManager(t *testing.T) {
	mgr, err := NewManager(ManagerOptions{
		Capture: CaptureOptions{
			Width:          320,
			Height:         180,
			ForceSynthetic: true,
		},
		PreferH264MFT: false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	var engine core.MediaEngine = mgr

	recPath := filepath.Join(t.TempDir(), "session.nxlr")
	if err := mgr.StartRecording(recPath, 320, 180); err != nil {
		t.Fatalf("StartRecording: %v", err)
	}

	frame, err := engine.CaptureScreen()
	if err != nil {
		t.Fatalf("CaptureScreen: %v", err)
	}
	if frame.Width != 320 || frame.Height != 180 || len(frame.Data) != 320*180*4 {
		t.Fatalf("unexpected captured frame: %dx%d len=%d", frame.Width, frame.Height, len(frame.Data))
	}
	if len(frame.NV12Y) == 0 || len(frame.NV12UV) == 0 {
		t.Fatalf("expected NV12 planes populated")
	}

	encoded, err := engine.EncodeFrame(frame, 80)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	decoded, err := engine.DecodeFrame(encoded)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if decoded.Width != frame.Width || decoded.Height != frame.Height {
		t.Fatalf("decoded dimension mismatch: %dx%d", decoded.Width, decoded.Height)
	}

	// Exercise live capture path (DXGI -> GDI -> synthetic fallback) without ForceSynthetic.
	liveCap := NewScreenCapturer(CaptureOptions{Width: 160, Height: 90})
	defer liveCap.Close()
	liveFrame, err := liveCap.CaptureScreen()
	if err != nil {
		t.Fatalf("live CaptureScreen: %v", err)
	}
	if liveFrame.Width <= 0 || liveFrame.Height <= 0 || len(liveFrame.Data) != liveFrame.Width*liveFrame.Height*4 {
		t.Fatalf("unexpected live captured frame: %dx%d len=%d", liveFrame.Width, liveFrame.Height, len(liveFrame.Data))
	}

	engine.SetVolume(0.5)
	if mgr.Volume() != 0.5 {
		t.Fatalf("expected volume 0.5, got %v", mgr.Volume())
	}

	gotAudio := make(chan *core.AudioFrame, 4)
	if err := engine.StartAudioCapture(false, func(af *core.AudioFrame) {
		select {
		case gotAudio <- af:
		default:
		}
	}); err != nil {
		t.Fatalf("StartAudioCapture: %v", err)
	}
	select {
	case af := <-gotAudio:
		if af == nil || af.SampleRate <= 0 || af.Channels <= 0 {
			t.Fatalf("invalid captured audio frame: %+v", af)
		}
		if _, err := mgr.EncodeAudioFrame(af); err != nil {
			t.Fatalf("EncodeAudioFrame: %v", err)
		}
		if err := engine.PlayAudioFrame(af); err != nil {
			t.Fatalf("PlayAudioFrame: %v", err)
		}
	case <-time.NewTimer(2 * time.Second).C:
		t.Fatalf("timed out waiting for audio frame")
	}
	if err := engine.StopAudioCapture(); err != nil {
		t.Fatalf("StopAudioCapture: %v", err)
	}
	if err := mgr.StopRecording(); err != nil {
		t.Fatalf("StopRecording: %v", err)
	}

	reader, err := OpenLessonReader(recPath)
	if err != nil {
		t.Fatalf("OpenLessonReader: %v", err)
	}
	defer reader.Close()
	chunks, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected >= 2 recorded chunks, got %d", len(chunks))
	}
}

func TestHybridVideoCodecMFTFallback(t *testing.T) {
	hybrid, err := NewHybridVideoCodec(true)
	if err != nil {
		t.Fatalf("NewHybridVideoCodec: %v", err)
	}
	defer hybrid.Close()

	// Odd dimensions force safe fallback from MFT to CodecFrameZstd.
	rgba := renderSyntheticRGBA(nil, 129, 73, 3)
	frame := &core.VideoFrame{
		Seq:       3,
		Codec:     core.CodecH264MFT,
		Width:     129,
		Height:    73,
		KeyFrame:  true,
		Timestamp: 2000,
		Data:      rgba,
	}
	enc, err := hybrid.Encode(frame, 80)
	if err != nil {
		t.Fatalf("HybridVideoCodec.Encode fallback: %v", err)
	}
	dec, err := hybrid.Decode(enc)
	if err != nil {
		t.Fatalf("HybridVideoCodec.Decode fallback: %v", err)
	}
	if dec.Codec != core.CodecFrameZstd {
		t.Fatalf("expected fallback codec %s, got %s", core.CodecFrameZstd, dec.Codec)
	}
	if !bytes.Equal(dec.Data, rgba) {
		t.Fatalf("fallback RGBA mismatch")
	}
}

func TestDownscaleFrameAndCaptureMax(t *testing.T) {
	rgba := renderSyntheticRGBA(nil, 1920, 1080, 5)
	src := &core.VideoFrame{
		Seq:       5,
		Codec:     core.CodecFrameZstd,
		Width:     1920,
		Height:    1080,
		KeyFrame:  true,
		Timestamp: 5000,
		Data:      rgba,
	}
	scaled := DownscaleFrame(src, 320, 180)
	if scaled.Width != 320 || scaled.Height != 180 || len(scaled.Data) != 320*180*4 {
		t.Fatalf("unexpected downscaled frame: %dx%d len=%d", scaled.Width, scaled.Height, len(scaled.Data))
	}

	mgr, err := NewManager(ManagerOptions{Capture: CaptureOptions{Width: 1280, Height: 720, ForceSynthetic: true}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	thumb, err := mgr.CaptureScreenMax(320, 180)
	if err != nil || thumb.Width != 320 || thumb.Height != 180 {
		t.Fatalf("CaptureScreenMax failed: err=%v frame=%+v", err, thumb)
	}
}

func TestZstdCodecRejectsDeltaSeqGapAndRecoversOnKeyframe(t *testing.T) {
	codec, err := NewZstdVideoCodec()
	if err != nil {
		t.Fatalf("NewZstdVideoCodec: %v", err)
	}
	defer codec.Close()

	const w, h = 160, 96
	f1 := &core.VideoFrame{
		Seq:       1,
		Codec:     core.CodecFrameZstd,
		Width:     w,
		Height:    h,
		KeyFrame:  true,
		Timestamp: 1000,
		Data:      renderSyntheticRGBA(nil, w, h, 1),
	}
	enc1, err := codec.Encode(f1, 85)
	if err != nil {
		t.Fatalf("Encode keyframe 1: %v", err)
	}
	if _, err := codec.Decode(enc1); err != nil {
		t.Fatalf("Decode keyframe 1: %v", err)
	}

	// Encode seq 2 and seq 3 as deltas, then drop seq 2 on decode.
	rgba2 := make([]byte, w*h*4)
	copy(rgba2, f1.Data)
	rgba2[0] ^= 0x55
	f2 := &core.VideoFrame{Seq: 2, Codec: core.CodecFrameZstd, Width: w, Height: h, Timestamp: 1033, Data: rgba2}
	if _, err := codec.Encode(f2, 85); err != nil {
		t.Fatalf("Encode delta 2: %v", err)
	}

	rgba3 := make([]byte, w*h*4)
	copy(rgba3, rgba2)
	rgba3[4] ^= 0x55
	f3 := &core.VideoFrame{Seq: 3, Codec: core.CodecFrameZstd, Width: w, Height: h, Timestamp: 1066, Data: rgba3}
	enc3, err := codec.Encode(f3, 85)
	if err != nil {
		t.Fatalf("Encode delta 3: %v", err)
	}

	if _, err := codec.Decode(enc3); err == nil {
		t.Fatalf("expected error decoding delta frame with seq gap 1 -> 3")
	}

	// Subsequent keyframe must resync cleanly.
	f4 := &core.VideoFrame{Seq: 4, Codec: core.CodecFrameZstd, Width: w, Height: h, KeyFrame: true, Timestamp: 1100, Data: renderSyntheticRGBA(nil, w, h, 4)}
	enc4, err := codec.Encode(f4, 85)
	if err != nil {
		t.Fatalf("Encode keyframe 4: %v", err)
	}
	dec4, err := codec.Decode(enc4)
	if err != nil {
		t.Fatalf("Decode keyframe 4 after seq gap: %v", err)
	}
	if !bytes.Equal(dec4.Data, f4.Data) {
		t.Fatalf("keyframe 4 data mismatch after recovery")
	}
}
