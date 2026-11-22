package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
	"unsafe"
)

func TestPwPodPrimitiveEncoding(t *testing.T) {
	want := []byte{4, 0, 0, 0, 3, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0}
	if got := pwPodID(2); !bytes.Equal(got, want) {
		t.Fatalf("pwPodID = %v, want %v", got, want)
	}
	choice := pwPodChoice(pwSpaChoiceRange, pwSpaTypeRectangle, 8, pwRect(1, 2), pwRect(3, 4), pwRect(5, 6))
	if size := binary.LittleEndian.Uint32(choice); size != 16+3*8 {
		t.Fatalf("choice body size = %d", size)
	}
	if typ := binary.LittleEndian.Uint32(choice[4:]); typ != pwSpaTypeChoice {
		t.Fatalf("choice type = %d", typ)
	}
	if len(choice)%8 != 0 {
		t.Fatalf("pod not 8-byte padded: %d", len(choice))
	}
	odd := pwPodChoice(pwSpaChoiceEnum, pwSpaTypeID, 4, pwU32s(1), pwU32s(2), pwU32s(3))
	if len(odd) != 8+pwPad8(16+12) {
		t.Fatalf("enum choice padding wrong: %d", len(odd))
	}
}

func TestPwEnumFormatRoundTrip(t *testing.T) {
	pod := pwBuildEnumFormat()
	if binary.LittleEndian.Uint32(pod[8:]) != pwSpaTypeObjectFormat || binary.LittleEndian.Uint32(pod[12:]) != pwSpaParamEnumFormat {
		t.Fatalf("unexpected object header %v", pod[:16])
	}
	if int(binary.LittleEndian.Uint32(pod))+8 != len(pod) {
		t.Fatalf("object size %d does not cover %d bytes", binary.LittleEndian.Uint32(pod), len(pod))
	}
	// Choices resolve to their default value.
	info, err := pwParseVideoFormat(pod)
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != pwVideoFormatBGRx || info.Width != 1920 || info.Height != 1080 || info.HasModifier {
		t.Fatalf("unexpected defaults %+v", info)
	}
}

func pwTestFormat(format, w, h uint32, extra ...pwPodProp) []byte {
	props := []pwPodProp{
		{pwSpaFormatMediaType, pwPodID(pwSpaMediaTypeVideo)},
		{pwSpaFormatMediaSubtype, pwPodID(pwSpaMediaSubtypeRaw)},
		{pwSpaFormatVideoFormat, pwPodID(format)},
		{pwSpaFormatVideoSize, pwPodRaw(pwSpaTypeRectangle, pwRect(w, h))},
		{pwSpaFormatVideoFramerate, pwPodRaw(pwSpaTypeFraction, pwFrac(0, 1))},
	}
	return pwPodObject(pwSpaTypeObjectFormat, pwSpaParamFormat, append(props, extra...)...)
}

func TestPwParseNegotiatedFormat(t *testing.T) {
	info, err := pwParseVideoFormat(pwTestFormat(pwVideoFormatRGBA, 2560, 1440))
	if err != nil {
		t.Fatal(err)
	}
	if info.Format != pwVideoFormatRGBA || info.Width != 2560 || info.Height != 1440 {
		t.Fatalf("unexpected info %+v", info)
	}
	withMod, err := pwParseVideoFormat(pwTestFormat(pwVideoFormatBGRx, 64, 32, pwPodProp{pwSpaFormatVideoModifier, pwPodRaw(5, make([]byte, 8))}))
	if err != nil || !withMod.HasModifier {
		t.Fatalf("modifier not detected: %+v %v", withMod, err)
	}
}

func TestPwParseRejectsBadPods(t *testing.T) {
	good := pwTestFormat(pwVideoFormatBGRx, 640, 480)
	for i := 0; i < len(good); i += 3 {
		if _, err := pwParseVideoFormat(good[:i]); err == nil {
			t.Fatalf("truncated pod (%d bytes) accepted", i)
		}
	}
	cases := map[string][]byte{
		"unsupported format": pwTestFormat(2 /* I420 */, 640, 480),
		"zero size":          pwTestFormat(pwVideoFormatBGRx, 0, 480),
		"huge size":          pwTestFormat(pwVideoFormatBGRx, 100000, 480),
		"not an object":      pwPodID(3),
		"buffers object":     pwBuildBuffersParam(),
	}
	for name, pod := range cases {
		if _, err := pwParseVideoFormat(pod); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	// Corrupt an inner value size so it points past the object.
	bad := append([]byte(nil), good...)
	binary.LittleEndian.PutUint32(bad[8+8+8:], 0xfffffff0)
	if _, err := pwParseVideoFormat(bad); !errors.Is(err, errPwPodTruncated) {
		t.Fatalf("expected truncation error, got %v", err)
	}
}

func TestPwBuffersParamAdvertisesCPUMemoryOnly(t *testing.T) {
	pod := pwBuildBuffersParam()
	if binary.LittleEndian.Uint32(pod[8:]) != pwSpaTypeObjectParamBuffers || binary.LittleEndian.Uint32(pod[12:]) != pwSpaParamBuffers {
		t.Fatalf("unexpected header")
	}
	if binary.LittleEndian.Uint32(pod[16:]) != pwSpaParamBuffersDataType {
		t.Fatalf("dataType key missing")
	}
	typ, v, err := pwPodValue(pod[24:])
	if err != nil || typ != pwSpaTypeInt {
		t.Fatalf("dataType value: typ=%d err=%v", typ, err)
	}
	mask := binary.LittleEndian.Uint32(v)
	if mask != 1<<pwSpaDataMemPtr|1<<pwSpaDataMemFd || mask&(1<<pwSpaDataDmaBuf) != 0 {
		t.Fatalf("unexpected data type mask %b", mask)
	}
}

func TestPwConvertToRGBAAllFormats(t *testing.T) {
	// One pixel with R=0x11 G=0x22 B=0x33 and junk alpha/padding 0x44.
	pixels := map[uint32][]byte{
		pwVideoFormatRGBx: {0x11, 0x22, 0x33, 0x44},
		pwVideoFormatRGBA: {0x11, 0x22, 0x33, 0x44},
		pwVideoFormatBGRx: {0x33, 0x22, 0x11, 0x44},
		pwVideoFormatBGRA: {0x33, 0x22, 0x11, 0x44},
		pwVideoFormatXRGB: {0x44, 0x11, 0x22, 0x33},
		pwVideoFormatARGB: {0x44, 0x11, 0x22, 0x33},
		pwVideoFormatXBGR: {0x44, 0x33, 0x22, 0x11},
		pwVideoFormatABGR: {0x44, 0x33, 0x22, 0x11},
		pwVideoFormatRGB:  {0x11, 0x22, 0x33},
		pwVideoFormatBGR:  {0x33, 0x22, 0x11},
	}
	want := []byte{0x11, 0x22, 0x33, 0xff}
	for format, px := range pixels {
		got, err := pwConvertToRGBA(nil, px, 1, 1, len(px), format)
		if err != nil {
			t.Fatalf("format %d: %v", format, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("format %d: got %x want %x", format, got, want)
		}
	}
	if _, err := pwConvertToRGBA(nil, make([]byte, 16), 1, 1, 4, 2); !errors.Is(err, errPwUnsupportedFmt) {
		t.Fatalf("expected unsupported format, got %v", err)
	}
}

func TestPwConvertHandlesStridePadding(t *testing.T) {
	// 2x2 BGRx with 4 bytes of row padding; last row has no padding.
	src := []byte{
		1, 2, 3, 0, 4, 5, 6, 0, 0xEE, 0xEE, 0xEE, 0xEE,
		7, 8, 9, 0, 10, 11, 12, 0,
	}
	got, err := pwConvertToRGBA(nil, src, 2, 2, 12, pwVideoFormatBGRx)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{3, 2, 1, 255, 6, 5, 4, 255, 9, 8, 7, 255, 12, 11, 10, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if _, err := pwConvertToRGBA(nil, src[:len(src)-1], 2, 2, 12, pwVideoFormatBGRx); !errors.Is(err, errPwBadGeometry) {
		t.Fatalf("short buffer accepted: %v", err)
	}
	if _, err := pwConvertToRGBA(nil, src, 2, 2, 7, pwVideoFormatBGRx); !errors.Is(err, errPwBadGeometry) {
		t.Fatalf("stride smaller than row accepted: %v", err)
	}
	// dst reuse
	buf := make([]byte, 0, 64)
	out, _ := pwConvertToRGBA(buf, src, 2, 2, 12, pwVideoFormatBGRx)
	if &out[0] != &buf[:1][0] {
		t.Fatalf("dst capacity was not reused")
	}
}

func TestPwFrameStoreResolutionChangesAndReset(t *testing.T) {
	var s pwFrameStore
	if _, _, _, _, ok, _ := s.snapshotRGBA(); ok {
		t.Fatal("empty store reported a frame")
	}
	padded := []byte{1, 2, 3, 0, 9, 9, 4, 5, 6, 0}
	if err := s.store(padded, 1, 2, 6, pwVideoFormatRGBx); err != nil {
		t.Fatal(err)
	}
	rgba, w, h, seq1, ok, err := s.snapshotRGBA()
	if err != nil || !ok || w != 1 || h != 2 || !bytes.Equal(rgba, []byte{1, 2, 3, 255, 4, 5, 6, 255}) {
		t.Fatalf("snapshot1: %v %d %d %v %v", rgba, w, h, ok, err)
	}
	// Resolution change, stride 0 means tightly packed.
	if err := s.store(make([]byte, 3*1*4), 3, 1, 0, pwVideoFormatBGRA); err != nil {
		t.Fatal(err)
	}
	_, w, h, seq2, ok, _ := s.snapshotRGBA()
	if !ok || w != 3 || h != 1 || seq2 <= seq1 {
		t.Fatalf("after resize: %dx%d seq %d->%d", w, h, seq1, seq2)
	}
	// A bad frame is rejected and leaves the previous one intact.
	if err := s.store(make([]byte, 3), 3, 1, 0, pwVideoFormatBGRA); err == nil {
		t.Fatal("short frame accepted")
	}
	if _, w, _, seq3, ok, _ := s.snapshotRGBA(); !ok || w != 3 || seq3 != seq2 {
		t.Fatal("previous frame lost after rejected store")
	}
	s.reset()
	if _, _, _, seq4, ok, _ := s.snapshotRGBA(); ok || seq4 != seq2 {
		t.Fatalf("reset: ok=%v seq=%d", ok, seq4)
	}
	if s.hasFrame() {
		t.Fatal("hasFrame after reset")
	}
}

func TestPwNativeStructLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("PipeWire bridge only targets LP64")
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"spa_data", unsafe.Sizeof(pwSpaData{}), 40},
		{"spa_data.data", unsafe.Offsetof(pwSpaData{}.Data), 24},
		{"spa_data.chunk", unsafe.Offsetof(pwSpaData{}.Chunk), 32},
		{"spa_chunk", unsafe.Sizeof(pwSpaChunk{}), 16},
		{"spa_buffer", unsafe.Sizeof(pwSpaBuffer{}), 24},
		{"spa_buffer.datas", unsafe.Offsetof(pwSpaBuffer{}.Datas), 16},
		{"pw_buffer.buffer", unsafe.Offsetof(pwBufferHeader{}.Buffer), 0},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Fatalf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}
