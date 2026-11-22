package media

// Pure-Go SPA (Simple Plugin API) helpers for the Linux PipeWire screen
// capture bridge in pipewire_linux.go. This file intentionally has no GOOS
// constraint: it contains no native calls, so the POD encoder/decoder, the
// pixel format conversion and the frame store are unit tested on every OS.
//
// The constants mirror the stable SPA ABI (spa/utils/type.h,
// spa/param/param.h, spa/param/format.h, spa/param/video/raw.h,
// spa/param/buffers.h, spa/buffer/buffer.h).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

const (
	pwSpaTypeID        = 3
	pwSpaTypeInt       = 4
	pwSpaTypeRectangle = 10
	pwSpaTypeFraction  = 11
	pwSpaTypeObject    = 15
	pwSpaTypeChoice    = 19

	pwSpaTypeObjectFormat       = 0x40003
	pwSpaTypeObjectParamBuffers = 0x40004

	pwSpaParamEnumFormat = 3
	pwSpaParamFormat     = 4
	pwSpaParamBuffers    = 5

	pwSpaFormatMediaType      = 1
	pwSpaFormatMediaSubtype   = 2
	pwSpaFormatVideoFormat    = 0x20001
	pwSpaFormatVideoModifier  = 0x20002
	pwSpaFormatVideoSize      = 0x20003
	pwSpaFormatVideoFramerate = 0x20004

	pwSpaMediaTypeVideo  = 2
	pwSpaMediaSubtypeRaw = 1

	pwSpaChoiceNone  = 0
	pwSpaChoiceRange = 1
	pwSpaChoiceStep  = 2
	pwSpaChoiceEnum  = 3
	pwSpaChoiceFlags = 4

	pwSpaParamBuffersDataType = 6

	pwSpaDataMemPtr = 1
	pwSpaDataMemFd  = 2
	pwSpaDataDmaBuf = 3

	pwSpaChunkFlagCorrupted = 1

	// enum spa_video_format (byte order in memory, GStreamer naming).
	pwVideoFormatRGBx = 7
	pwVideoFormatBGRx = 8
	pwVideoFormatXRGB = 9
	pwVideoFormatXBGR = 10
	pwVideoFormatRGBA = 11
	pwVideoFormatBGRA = 12
	pwVideoFormatARGB = 13
	pwVideoFormatABGR = 14
	pwVideoFormatRGB  = 15
	pwVideoFormatBGR  = 16

	// pwMaxVideoDimension bounds sizes accepted from the compositor so a bogus
	// format cannot make us allocate unbounded memory.
	pwMaxVideoDimension = 16384
	// pwMaxPodSize bounds how much memory is read behind a native spa_pod.
	pwMaxPodSize = 1 << 16
)

var (
	errPwPodTruncated   = errors.New("media: truncated SPA pod")
	errPwPodNotFormat   = errors.New("media: SPA pod is not a video format object")
	errPwUnsupportedFmt = errors.New("media: unsupported PipeWire video format")
	errPwBadGeometry    = errors.New("media: invalid PipeWire frame geometry")
)

// pwOfferedVideoFormats lists the raw formats announced in EnumFormat; the
// first entry is the preferred default (what mutter/KWin produce natively).
var pwOfferedVideoFormats = []uint32{
	pwVideoFormatBGRx, pwVideoFormatBGRA, pwVideoFormatRGBx, pwVideoFormatRGBA,
	pwVideoFormatXRGB, pwVideoFormatXBGR, pwVideoFormatARGB, pwVideoFormatABGR,
}

// Native struct mirrors (LP64 layout: amd64, arm64, loong64). pipewire_linux.go
// refuses to run on 32-bit targets.

// pwBufferHeader mirrors the leading fields of struct pw_buffer.
type pwBufferHeader struct {
	Buffer   uintptr // struct spa_buffer *
	UserData uintptr
	Size     uint64
}

// pwSpaBuffer mirrors struct spa_buffer.
type pwSpaBuffer struct {
	NMetas uint32
	NDatas uint32
	Metas  uintptr
	Datas  uintptr
}

// pwSpaData mirrors struct spa_data.
type pwSpaData struct {
	Type      uint32
	Flags     uint32
	Fd        int64
	MapOffset uint32
	MaxSize   uint32
	Data      uintptr
	Chunk     uintptr
}

// pwSpaChunk mirrors struct spa_chunk.
type pwSpaChunk struct {
	Offset uint32
	Size   uint32
	Stride int32
	Flags  int32
}

// --- POD encoding -----------------------------------------------------------

func pwPad8(n int) int { return (n + 7) &^ 7 }

func pwPodRaw(typ uint32, body []byte) []byte {
	out := make([]byte, 8+pwPad8(len(body)))
	binary.LittleEndian.PutUint32(out[0:], uint32(len(body)))
	binary.LittleEndian.PutUint32(out[4:], typ)
	copy(out[8:], body)
	return out
}

func pwU32s(values ...uint32) []byte {
	out := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(out[4*i:], v)
	}
	return out
}

func pwPodID(v uint32) []byte   { return pwPodRaw(pwSpaTypeID, pwU32s(v)) }
func pwPodInt(v int32) []byte   { return pwPodRaw(pwSpaTypeInt, pwU32s(uint32(v))) }
func pwRect(w, h uint32) []byte { return pwU32s(w, h) }
func pwFrac(n, d uint32) []byte { return pwU32s(n, d) }

// pwPodChoice encodes a spa_pod_choice whose children are raw values of
// childType, each childSize bytes long.
func pwPodChoice(choiceType, childType, childSize uint32, values ...[]byte) []byte {
	body := pwU32s(choiceType, 0, childSize, childType)
	for _, v := range values {
		body = append(body, v[:childSize]...)
	}
	return pwPodRaw(pwSpaTypeChoice, body)
}

type pwPodProp struct {
	key   uint32
	value []byte
}

func pwPodObject(objType, id uint32, props ...pwPodProp) []byte {
	body := pwU32s(objType, id)
	for _, p := range props {
		body = append(body, pwU32s(p.key, 0)...)
		body = append(body, p.value...)
	}
	return pwPodRaw(pwSpaTypeObject, body)
}

// pwBuildEnumFormat builds the EnumFormat param offered when connecting the
// stream: raw video, CPU-mappable RGB formats, any size, any framerate. No
// modifier property is announced, so compositors fall back to shared-memory
// (MemFd/MemPtr) buffers instead of DMA-BUF.
func pwBuildEnumFormat() []byte {
	formats := make([][]byte, 0, len(pwOfferedVideoFormats)+1)
	formats = append(formats, pwU32s(pwOfferedVideoFormats[0]))
	for _, f := range pwOfferedVideoFormats {
		formats = append(formats, pwU32s(f))
	}
	return pwPodObject(pwSpaTypeObjectFormat, pwSpaParamEnumFormat,
		pwPodProp{pwSpaFormatMediaType, pwPodID(pwSpaMediaTypeVideo)},
		pwPodProp{pwSpaFormatMediaSubtype, pwPodID(pwSpaMediaSubtypeRaw)},
		pwPodProp{pwSpaFormatVideoFormat, pwPodChoice(pwSpaChoiceEnum, pwSpaTypeID, 4, formats...)},
		pwPodProp{pwSpaFormatVideoSize, pwPodChoice(pwSpaChoiceRange, pwSpaTypeRectangle, 8,
			pwRect(1920, 1080), pwRect(1, 1), pwRect(pwMaxVideoDimension, pwMaxVideoDimension))},
		pwPodProp{pwSpaFormatVideoFramerate, pwPodChoice(pwSpaChoiceRange, pwSpaTypeFraction, 8,
			pwFrac(30, 1), pwFrac(0, 1), pwFrac(1000, 1))},
	)
}

// pwBuildBuffersParam answers a negotiated Format with the buffer data types
// we can read from the CPU.
func pwBuildBuffersParam() []byte {
	mask := uint32(1<<pwSpaDataMemPtr | 1<<pwSpaDataMemFd)
	return pwPodObject(pwSpaTypeObjectParamBuffers, pwSpaParamBuffers,
		pwPodProp{pwSpaParamBuffersDataType, pwPodChoice(pwSpaChoiceFlags, pwSpaTypeInt, 4, pwU32s(mask))},
	)
}

// --- POD decoding -----------------------------------------------------------

// pwVideoFormatInfo is the negotiated raw video format.
type pwVideoFormatInfo struct {
	MediaType    uint32
	MediaSubtype uint32
	Format       uint32
	Width        int
	Height       int
	HasModifier  bool
}

// pwPodValue returns the type and body of a pod value, resolving a Choice to
// its first (default/fixed) value.
func pwPodValue(pod []byte) (uint32, []byte, error) {
	if len(pod) < 8 {
		return 0, nil, errPwPodTruncated
	}
	size := int(binary.LittleEndian.Uint32(pod[0:]))
	typ := binary.LittleEndian.Uint32(pod[4:])
	if size < 0 || size > len(pod)-8 {
		return 0, nil, errPwPodTruncated
	}
	body := pod[8 : 8+size]
	if typ != pwSpaTypeChoice {
		return typ, body, nil
	}
	if len(body) < 16 {
		return 0, nil, errPwPodTruncated
	}
	childSize := int(binary.LittleEndian.Uint32(body[8:]))
	childType := binary.LittleEndian.Uint32(body[12:])
	if childSize <= 0 || childSize > len(body)-16 {
		return 0, nil, errPwPodTruncated
	}
	return childType, body[16 : 16+childSize], nil
}

// pwParseVideoFormat decodes a Format object as delivered in param_changed.
func pwParseVideoFormat(pod []byte) (pwVideoFormatInfo, error) {
	var info pwVideoFormatInfo
	if len(pod) < 16 {
		return info, errPwPodTruncated
	}
	size := int(binary.LittleEndian.Uint32(pod[0:]))
	if binary.LittleEndian.Uint32(pod[4:]) != pwSpaTypeObject {
		return info, errPwPodNotFormat
	}
	if size < 8 || size > len(pod)-8 {
		return info, errPwPodTruncated
	}
	body := pod[8 : 8+size]
	if binary.LittleEndian.Uint32(body[0:]) != pwSpaTypeObjectFormat {
		return info, errPwPodNotFormat
	}
	rest := body[8:]
	for len(rest) > 0 {
		if len(rest) < 16 {
			return info, errPwPodTruncated
		}
		key := binary.LittleEndian.Uint32(rest[0:])
		value := rest[8:]
		valueSize := int(binary.LittleEndian.Uint32(value[0:]))
		if valueSize < 0 || valueSize > len(value)-8 {
			return info, errPwPodTruncated
		}
		typ, v, err := pwPodValue(value[:8+valueSize])
		if err != nil {
			return info, err
		}
		switch key {
		case pwSpaFormatMediaType:
			if typ == pwSpaTypeID && len(v) >= 4 {
				info.MediaType = binary.LittleEndian.Uint32(v)
			}
		case pwSpaFormatMediaSubtype:
			if typ == pwSpaTypeID && len(v) >= 4 {
				info.MediaSubtype = binary.LittleEndian.Uint32(v)
			}
		case pwSpaFormatVideoFormat:
			if typ == pwSpaTypeID && len(v) >= 4 {
				info.Format = binary.LittleEndian.Uint32(v)
			}
		case pwSpaFormatVideoSize:
			if typ == pwSpaTypeRectangle && len(v) >= 8 {
				info.Width = int(binary.LittleEndian.Uint32(v[0:]))
				info.Height = int(binary.LittleEndian.Uint32(v[4:]))
			}
		case pwSpaFormatVideoModifier:
			info.HasModifier = true
		}
		next := 8 + 8 + pwPad8(valueSize)
		if next > len(rest) {
			next = len(rest)
		}
		rest = rest[next:]
	}
	if info.MediaType != pwSpaMediaTypeVideo || info.MediaSubtype != pwSpaMediaSubtypeRaw {
		return info, fmt.Errorf("%w: media type %d/%d", errPwPodNotFormat, info.MediaType, info.MediaSubtype)
	}
	if pwBytesPerPixel(info.Format) == 0 {
		return info, fmt.Errorf("%w: %d", errPwUnsupportedFmt, info.Format)
	}
	if info.Width <= 0 || info.Height <= 0 || info.Width > pwMaxVideoDimension || info.Height > pwMaxVideoDimension {
		return info, fmt.Errorf("%w: %dx%d", errPwBadGeometry, info.Width, info.Height)
	}
	return info, nil
}

// --- pixel conversion -------------------------------------------------------

// pwChannelLayout returns byte offsets of R, G, B within one pixel and the
// pixel size; ok is false for formats we cannot convert.
func pwChannelLayout(format uint32) (r, g, b, bpp int, ok bool) {
	switch format {
	case pwVideoFormatRGBx, pwVideoFormatRGBA:
		return 0, 1, 2, 4, true
	case pwVideoFormatBGRx, pwVideoFormatBGRA:
		return 2, 1, 0, 4, true
	case pwVideoFormatXRGB, pwVideoFormatARGB:
		return 1, 2, 3, 4, true
	case pwVideoFormatXBGR, pwVideoFormatABGR:
		return 3, 2, 1, 4, true
	case pwVideoFormatRGB:
		return 0, 1, 2, 3, true
	case pwVideoFormatBGR:
		return 2, 1, 0, 3, true
	}
	return 0, 0, 0, 0, false
}

func pwBytesPerPixel(format uint32) int {
	_, _, _, bpp, ok := pwChannelLayout(format)
	if !ok {
		return 0
	}
	return bpp
}

// pwPlaneSpan validates a strided plane and returns the minimal number of bytes
// it occupies (the last row need not include stride padding).
func pwPlaneSpan(width, height, stride, bpp int) (int, error) {
	if width <= 0 || height <= 0 || bpp <= 0 || width > pwMaxVideoDimension || height > pwMaxVideoDimension {
		return 0, fmt.Errorf("%w: %dx%d", errPwBadGeometry, width, height)
	}
	if stride < width*bpp {
		return 0, fmt.Errorf("%w: stride %d < %d", errPwBadGeometry, stride, width*bpp)
	}
	return stride*(height-1) + width*bpp, nil
}

// pwConvertToRGBA converts a strided frame in format into tightly packed opaque
// RGBA (the core.VideoFrame layout), reusing dst when it is large enough.
func pwConvertToRGBA(dst, src []byte, width, height, stride int, format uint32) ([]byte, error) {
	r, g, b, bpp, ok := pwChannelLayout(format)
	if !ok {
		return nil, fmt.Errorf("%w: %d", errPwUnsupportedFmt, format)
	}
	span, err := pwPlaneSpan(width, height, stride, bpp)
	if err != nil {
		return nil, err
	}
	if len(src) < span {
		return nil, fmt.Errorf("%w: have %d bytes, need %d", errPwBadGeometry, len(src), span)
	}
	need := width * height * 4
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]
	out := 0
	for row := 0; row < height; row++ {
		line := src[row*stride : row*stride+width*bpp]
		for col := 0; col+bpp <= len(line); col += bpp {
			dst[out] = line[col+r]
			dst[out+1] = line[col+g]
			dst[out+2] = line[col+b]
			dst[out+3] = 255
			out += 4
		}
	}
	return dst, nil
}

// --- frame store ------------------------------------------------------------

// pwFrameStore keeps the newest raw frame delivered by the capture source. The
// PipeWire loop thread writes, Capture() reads; only real frames are stored.
type pwFrameStore struct {
	mu     sync.Mutex
	raw    []byte // tightly packed rows, width*bpp per row
	width  int
	height int
	format uint32
	seq    uint64
}

// store copies a strided frame, removing row padding. It returns an error and
// leaves the previous frame untouched when the geometry is inconsistent.
func (s *pwFrameStore) store(src []byte, width, height, stride int, format uint32) error {
	bpp := pwBytesPerPixel(format)
	if bpp == 0 {
		return fmt.Errorf("%w: %d", errPwUnsupportedFmt, format)
	}
	if stride == 0 {
		stride = width * bpp
	}
	span, err := pwPlaneSpan(width, height, stride, bpp)
	if err != nil {
		return err
	}
	if len(src) < span {
		return fmt.Errorf("%w: have %d bytes, need %d", errPwBadGeometry, len(src), span)
	}
	rowBytes := width * bpp
	s.mu.Lock()
	defer s.mu.Unlock()
	need := rowBytes * height
	if cap(s.raw) < need {
		s.raw = make([]byte, need)
	}
	s.raw = s.raw[:need]
	if stride == rowBytes {
		copy(s.raw, src[:need])
	} else {
		for row := 0; row < height; row++ {
			copy(s.raw[row*rowBytes:(row+1)*rowBytes], src[row*stride:row*stride+rowBytes])
		}
	}
	s.width, s.height, s.format = width, height, format
	s.seq++
	return nil
}

// snapshotRGBA converts the newest frame into a fresh RGBA buffer. ok is false
// when no frame has been received since the last reset.
func (s *pwFrameStore) snapshotRGBA() (rgba []byte, width, height int, seq uint64, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq == 0 || len(s.raw) == 0 {
		return nil, 0, 0, s.seq, false, nil
	}
	bpp := pwBytesPerPixel(s.format)
	rgba, err = pwConvertToRGBA(nil, s.raw, s.width, s.height, s.width*bpp, s.format)
	if err != nil {
		return nil, 0, 0, s.seq, false, err
	}
	return rgba, s.width, s.height, s.seq, true, nil
}

// reset drops the stored frame (new stream, new format). The sequence keeps
// increasing so callers never mistake a later frame for an old one.
func (s *pwFrameStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = s.raw[:0]
	s.width, s.height, s.format = 0, 0, 0
}

func (s *pwFrameStore) hasFrame() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.raw) > 0
}

func (s *pwFrameStore) sequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}
