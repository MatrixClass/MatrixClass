//go:build windows

package media

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"nexlab/internal/core"
)

var (
	ole32DLL  = windows.NewLazySystemDLL("ole32.dll")
	mfplatDLL = windows.NewLazySystemDLL("mfplat.dll")

	procCoInitializeEx    = ole32DLL.NewProc("CoInitializeEx")
	procCoCreateInstance  = ole32DLL.NewProc("CoCreateInstance")
	procMFStartup         = mfplatDLL.NewProc("MFStartup")
	procMFShutdown        = mfplatDLL.NewProc("MFShutdown")
	procMFCreateMediaType = mfplatDLL.NewProc("MFCreateMediaType")
	procMFCreateSample    = mfplatDLL.NewProc("MFCreateSample")
	procMFCreateMemBuf    = mfplatDLL.NewProc("MFCreateMemoryBuffer")
)

const (
	coinitMultithreaded   = 0
	clsctxInprocServer    = 1
	mfVersion             = 0x00020070
	mfStartupNoSocket     = 1
	mfVideoInterlaceProg  = 2
	mftMsgBeginStreaming  = 0x10000000
	mftMsgStartOfStream   = 0x10000003
	mftOutProvidesSamples = 0x100
	mfENeedMoreInput      = 0xC00D6D72
	mfEStreamChange       = 0xC00D6D61
)

var (
	clsidCMSH264EncoderMFT = windows.GUID{Data1: 0x6ca50344, Data2: 0x051a, Data3: 0x4ded, Data4: [8]byte{0x97, 0x79, 0xa4, 0x33, 0x05, 0x16, 0x5e, 0x35}}
	clsidCMSH264DecoderMFT = windows.GUID{Data1: 0x62ce7e72, Data2: 0x4c71, Data3: 0x4d20, Data4: [8]byte{0xb1, 0x5d, 0x45, 0x28, 0x31, 0xa8, 0x7d, 0x9d}}
	iidIMFTransform        = windows.GUID{Data1: 0xbf94c121, Data2: 0x5b05, Data3: 0x4e6f, Data4: [8]byte{0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d}}

	mfMediaTypeVideo     = windows.GUID{Data1: 0x73646976, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfVideoFormatNV12    = windows.GUID{Data1: 0x3231564e, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfVideoFormatH264    = windows.GUID{Data1: 0x34363248, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}}
	mfMTMajorType        = windows.GUID{Data1: 0x48eba18e, Data2: 0xf8c9, Data3: 0x4687, Data4: [8]byte{0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f}}
	mfMTSubtype          = windows.GUID{Data1: 0xf7e34c9a, Data2: 0x42e8, Data3: 0x4714, Data4: [8]byte{0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5}}
	mfMTAvgBitrate       = windows.GUID{Data1: 0x20332624, Data2: 0xfb0d, Data3: 0x4d9e, Data4: [8]byte{0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e}}
	mfMTInterlaceMode    = windows.GUID{Data1: 0xe2724bb8, Data2: 0xe676, Data3: 0x4806, Data4: [8]byte{0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd}}
	mfMTFrameSize        = windows.GUID{Data1: 0x1652c33d, Data2: 0xd6b2, Data3: 0x4012, Data4: [8]byte{0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d}}
	mfMTFrameRate        = windows.GUID{Data1: 0xc459a2e8, Data2: 0x3d2c, Data3: 0x4e44, Data4: [8]byte{0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0}}
	mfMTPixelAspectRatio = windows.GUID{Data1: 0xc6376a1e, Data2: 0x8d0a, Data3: 0x4027, Data4: [8]byte{0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6}}
)

type mftOutputStreamInfo struct {
	DwFlags     uint32
	CbSize      uint32
	CbAlignment uint32
}

type mftOutputDataBuffer struct {
	DwStreamID uint32
	PSample    uintptr
	DwStatus   uint32
	PEvents    uintptr
}

// windowsH264MFT wraps Windows Media Foundation's H.264 encoder and decoder MFTs.
// Any initialization or runtime error safely triggers fallback to ZstdVideoCodec.
type windowsH264MFT struct {
	mu        sync.Mutex
	mfStarted bool
	available bool

	encMFT     uintptr
	encW       int
	encH       int
	encQuality int
	encTime    int64

	decMFT uintptr
	decW   int
	decH   int
}

func newPlatformVideoMFT() videoMFTBackend {
	m := &windowsH264MFT{}
	_ = m.ensureMF()
	return m
}

func (m *windowsH264MFT) Available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available
}

func (m *windowsH264MFT) ensureMF() error {
	if m.mfStarted {
		if m.available {
			return nil
		}
		return ErrMFTUnavailable
	}
	if err := ole32DLL.Load(); err != nil {
		return ErrMFTUnavailable
	}
	if err := mfplatDLL.Load(); err != nil {
		return ErrMFTUnavailable
	}
	_, _, _ = procCoInitializeEx.Call(0, coinitMultithreaded)
	hr, _, _ := procMFStartup.Call(mfVersion, mfStartupNoSocket)
	if int32(hr) < 0 {
		m.mfStarted = true
		m.available = false
		return fmt.Errorf("%w: MFStartup 0x%08x", ErrMFTUnavailable, uint32(hr))
	}
	m.mfStarted = true

	var probe uintptr
	hr, _, _ = procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCMSH264EncoderMFT)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIMFTransform)),
		uintptr(unsafe.Pointer(&probe)),
	)
	if int32(hr) >= 0 && probe != 0 {
		comRelease(probe)
		m.available = true
		return nil
	}
	m.available = false
	return ErrMFTUnavailable
}

func (m *windowsH264MFT) Encode(frame *core.VideoFrame, quality int) ([]byte, error) {
	if frame == nil || frame.Width <= 0 || frame.Height <= 0 {
		return nil, ErrInvalidDimension
	}
	if frame.Width%2 != 0 || frame.Height%2 != 0 {
		return nil, ErrMFTUnavailable
	}
	if !validWindowsFrameDimensions(frame.Width, frame.Height) {
		return nil, ErrInvalidDimension
	}
	if quality <= 0 {
		quality = 80
	}
	if quality > 100 {
		quality = 100
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureMF(); err != nil {
		return nil, err
	}
	if err := m.ensureEncoder(frame.Width, frame.Height, quality); err != nil {
		return nil, err
	}
	if err := EnsureNV12(frame); err != nil {
		return nil, err
	}

	w, h := frame.Width, frame.Height
	ch := (h + 1) / 2
	nv12 := make([]byte, w*h+w*ch)
	for row := 0; row < h; row++ {
		copy(nv12[row*w:(row+1)*w], frame.NV12Y[row*frame.StrideY:row*frame.StrideY+w])
	}
	uvBase := w * h
	for row := 0; row < ch; row++ {
		copy(nv12[uvBase+row*w:uvBase+(row+1)*w], frame.NV12UV[row*frame.StrideUV:row*frame.StrideUV+w])
	}

	sample, err := createMFSampleFromBytes(nv12, m.encTime, 333333)
	if err != nil {
		return nil, err
	}
	defer comRelease(sample)
	m.encTime += 333333

	hr := comCall(m.encMFT, 24, 0, sample, 0)
	if int32(hr) < 0 {
		m.releaseEncoder()
		return nil, fmt.Errorf("%w: ProcessInput 0x%08x", ErrMFTUnavailable, uint32(hr))
	}

	outBytes, err := m.drainOneOutput(m.encMFT)
	if err != nil {
		return nil, err
	}
	if len(outBytes) == 0 {
		return nil, fmt.Errorf("%w: empty MFT output", ErrMFTUnavailable)
	}
	var flags uint8
	if frame.KeyFrame {
		flags |= videoFlagKeyFrame
	}
	return packVideoHeader(codecIDH264MFT, flags, uint8(quality), w, h, frame.Seq, frame.Timestamp, outBytes), nil
}

func (m *windowsH264MFT) Decode(data []byte) (*core.VideoFrame, error) {
	codecID, flags, _, w, h, seq, ts, payload, err := parseVideoHeader(data)
	if err != nil {
		return nil, err
	}
	if codecID != codecIDH264MFT {
		return nil, fmt.Errorf("%w: unexpected codec id %d", ErrInvalidFrameData, codecID)
	}
	if w <= 0 || h <= 0 {
		return nil, ErrInvalidDimension
	}
	if !validWindowsFrameDimensions(w, h) {
		return nil, ErrInvalidDimension
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureMF(); err != nil {
		return nil, err
	}
	if err := m.ensureDecoder(w, h); err != nil {
		return nil, err
	}

	sample, err := createMFSampleFromBytes(payload, ts*10000, 333333)
	if err != nil {
		return nil, err
	}
	defer comRelease(sample)

	hr := comCall(m.decMFT, 24, 0, sample, 0)
	if int32(hr) < 0 {
		m.releaseDecoder()
		return nil, fmt.Errorf("%w: decoder ProcessInput 0x%08x", ErrMFTUnavailable, uint32(hr))
	}

	nv12, err := m.drainOneOutput(m.decMFT)
	if err != nil {
		return nil, err
	}
	ch := (h + 1) / 2
	uvStride := ((w + 1) / 2) * 2
	need := w*h + uvStride*ch
	if len(nv12) < need {
		return nil, ErrMFTUnavailable
	}
	yPlane := make([]byte, w*h)
	uvPlane := make([]byte, uvStride*ch)
	copy(yPlane, nv12[:w*h])
	copy(uvPlane, nv12[w*h:need])
	rgba, err := NV12ToRGBA(yPlane, uvPlane, w, h, w, uvStride)
	if err != nil {
		return nil, err
	}
	return &core.VideoFrame{
		Seq:       seq,
		Codec:     core.CodecH264MFT,
		Width:     w,
		Height:    h,
		KeyFrame:  flags&videoFlagKeyFrame != 0,
		Timestamp: ts,
		Data:      rgba,
		NV12Y:     yPlane,
		NV12UV:    uvPlane,
		StrideY:   w,
		StrideUV:  uvStride,
	}, nil
}

func (m *windowsH264MFT) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseEncoder()
	m.releaseDecoder()
	if m.mfStarted {
		_, _, _ = procMFShutdown.Call()
		m.mfStarted = false
	}
	m.available = false
	return nil
}

func (m *windowsH264MFT) ensureEncoder(w, h, quality int) error {
	if m.encMFT != 0 && m.encW == w && m.encH == h && m.encQuality == quality {
		return nil
	}
	m.releaseEncoder()

	var mft uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCMSH264EncoderMFT)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIMFTransform)),
		uintptr(unsafe.Pointer(&mft)),
	)
	if int32(hr) < 0 || mft == 0 {
		return ErrMFTUnavailable
	}

	bitrate := uint32(1_500_000 + quality*45_000)
	outType, err := createVideoMediaType(mfVideoFormatH264, w, h, 30, bitrate)
	if err != nil {
		comRelease(mft)
		return err
	}
	defer comRelease(outType)
	if r := comCall(mft, 16, 0, outType, 0); int32(r) < 0 {
		comRelease(mft)
		return fmt.Errorf("%w: SetOutputType(H264) 0x%08x", ErrMFTUnavailable, uint32(r))
	}

	inType, err := createVideoMediaType(mfVideoFormatNV12, w, h, 30, 0)
	if err != nil {
		comRelease(mft)
		return err
	}
	defer comRelease(inType)
	if r := comCall(mft, 15, 0, inType, 0); int32(r) < 0 {
		comRelease(mft)
		return fmt.Errorf("%w: SetInputType(NV12) 0x%08x", ErrMFTUnavailable, uint32(r))
	}

	comCall(mft, 23, mftMsgBeginStreaming, 0)
	comCall(mft, 23, mftMsgStartOfStream, 0)
	m.encMFT = mft
	m.encW, m.encH, m.encQuality = w, h, quality
	return nil
}

func (m *windowsH264MFT) ensureDecoder(w, h int) error {
	if m.decMFT != 0 && m.decW == w && m.decH == h {
		return nil
	}
	m.releaseDecoder()

	var mft uintptr
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidCMSH264DecoderMFT)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIMFTransform)),
		uintptr(unsafe.Pointer(&mft)),
	)
	if int32(hr) < 0 || mft == 0 {
		return ErrMFTUnavailable
	}

	inType, err := createVideoMediaType(mfVideoFormatH264, w, h, 30, 0)
	if err != nil {
		comRelease(mft)
		return err
	}
	defer comRelease(inType)
	if r := comCall(mft, 15, 0, inType, 0); int32(r) < 0 {
		comRelease(mft)
		return fmt.Errorf("%w: decoder SetInputType(H264) 0x%08x", ErrMFTUnavailable, uint32(r))
	}

	outType, err := createVideoMediaType(mfVideoFormatNV12, w, h, 30, 0)
	if err != nil {
		comRelease(mft)
		return err
	}
	defer comRelease(outType)
	if r := comCall(mft, 16, 0, outType, 0); int32(r) < 0 {
		comRelease(mft)
		return fmt.Errorf("%w: decoder SetOutputType(NV12) 0x%08x", ErrMFTUnavailable, uint32(r))
	}

	comCall(mft, 23, mftMsgBeginStreaming, 0)
	comCall(mft, 23, mftMsgStartOfStream, 0)
	m.decMFT = mft
	m.decW, m.decH = w, h
	return nil
}

func (m *windowsH264MFT) releaseEncoder() {
	if m.encMFT != 0 {
		comRelease(m.encMFT)
		m.encMFT = 0
	}
	m.encW, m.encH, m.encQuality = 0, 0, 0
}

func (m *windowsH264MFT) releaseDecoder() {
	if m.decMFT != 0 {
		comRelease(m.decMFT)
		m.decMFT = 0
	}
	m.decW, m.decH = 0, 0
}

func (m *windowsH264MFT) drainOneOutput(mft uintptr) ([]byte, error) {
	var info mftOutputStreamInfo
	if r := comCall(mft, 7, 0, uintptr(unsafe.Pointer(&info))); int32(r) < 0 {
		return nil, fmt.Errorf("%w: GetOutputStreamInfo 0x%08x", ErrMFTUnavailable, uint32(r))
	}

	var outSample uintptr
	if info.DwFlags&mftOutProvidesSamples == 0 {
		size := info.CbSize
		if size < 65536 {
			size = 65536
		}
		var buf uintptr
		hr, _, _ := procMFCreateMemBuf.Call(uintptr(size), uintptr(unsafe.Pointer(&buf)))
		if int32(hr) < 0 || buf == 0 {
			return nil, ErrMFTUnavailable
		}
		hr, _, _ = procMFCreateSample.Call(uintptr(unsafe.Pointer(&outSample)))
		if int32(hr) < 0 || outSample == 0 {
			comRelease(buf)
			return nil, ErrMFTUnavailable
		}
		comCall(outSample, 42, buf)
		comRelease(buf)
	}

	var odb mftOutputDataBuffer
	odb.PSample = outSample
	var status uint32
	hr := comCall(mft, 25, 0, 1, uintptr(unsafe.Pointer(&odb)), uintptr(unsafe.Pointer(&status)))
	if odb.PEvents != 0 {
		comRelease(odb.PEvents)
	}
	if uint32(hr) == mfENeedMoreInput || uint32(hr) == mfEStreamChange {
		if odb.PSample != 0 {
			comRelease(odb.PSample)
		}
		return nil, ErrMFTUnavailable
	}
	if int32(hr) < 0 || odb.PSample == 0 {
		if odb.PSample != 0 {
			comRelease(odb.PSample)
		}
		return nil, fmt.Errorf("%w: ProcessOutput 0x%08x", ErrMFTUnavailable, uint32(hr))
	}
	defer comRelease(odb.PSample)

	var contig uintptr
	if r := comCall(odb.PSample, 41, uintptr(unsafe.Pointer(&contig))); int32(r) < 0 || contig == 0 {
		return nil, ErrMFTUnavailable
	}
	defer comRelease(contig)

	var ptr uintptr
	var maxLen, curLen uint32
	if r := comCall(contig, 3, uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen))); int32(r) < 0 || ptr == 0 {
		return nil, ErrMFTUnavailable
	}
	if curLen > maxLen {
		return nil, ErrBufferTooSmall
	}
	defer comCall(contig, 4)

	out := make([]byte, curLen)
	if curLen > 0 {
		copy(out, unsafe.Slice((*byte)(unsafe.Pointer(ptr)), curLen))
	}
	return out, nil
}

func createVideoMediaType(subtype windows.GUID, width, height, fps int, bitrate uint32) (uintptr, error) {
	var mt uintptr
	hr, _, _ := procMFCreateMediaType.Call(uintptr(unsafe.Pointer(&mt)))
	if int32(hr) < 0 || mt == 0 {
		return 0, ErrMFTUnavailable
	}
	comCall(mt, 24, uintptr(unsafe.Pointer(&mfMTMajorType)), uintptr(unsafe.Pointer(&mfMediaTypeVideo)))
	comCall(mt, 24, uintptr(unsafe.Pointer(&mfMTSubtype)), uintptr(unsafe.Pointer(&subtype)))
	comCall(mt, 21, uintptr(unsafe.Pointer(&mfMTInterlaceMode)), mfVideoInterlaceProg)
	frameSize := (uint64(uint32(width)) << 32) | uint64(uint32(height))
	comCall(mt, 22, uintptr(unsafe.Pointer(&mfMTFrameSize)), uintptr(frameSize))
	frameRate := (uint64(uint32(fps)) << 32) | 1
	comCall(mt, 22, uintptr(unsafe.Pointer(&mfMTFrameRate)), uintptr(frameRate))
	par := (uint64(1) << 32) | 1
	comCall(mt, 22, uintptr(unsafe.Pointer(&mfMTPixelAspectRatio)), uintptr(par))
	if bitrate > 0 {
		comCall(mt, 21, uintptr(unsafe.Pointer(&mfMTAvgBitrate)), uintptr(bitrate))
	}
	return mt, nil
}

func createMFSampleFromBytes(data []byte, sampleTime, duration int64) (uintptr, error) {
	var buf uintptr
	hr, _, _ := procMFCreateMemBuf.Call(uintptr(len(data)), uintptr(unsafe.Pointer(&buf)))
	if int32(hr) < 0 || buf == 0 {
		return 0, ErrMFTUnavailable
	}
	var ptr uintptr
	var maxLen, curLen uint32
	if r := comCall(buf, 3, uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&maxLen)), uintptr(unsafe.Pointer(&curLen))); int32(r) < 0 || ptr == 0 {
		comRelease(buf)
		return 0, ErrMFTUnavailable
	}
	if uint64(len(data)) > uint64(maxLen) {
		comRelease(buf)
		return 0, ErrBufferTooSmall
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(ptr)), len(data)), data)
	comCall(buf, 4)
	comCall(buf, 6, uintptr(len(data)))

	var sample uintptr
	hr, _, _ = procMFCreateSample.Call(uintptr(unsafe.Pointer(&sample)))
	if int32(hr) < 0 || sample == 0 {
		comRelease(buf)
		return 0, ErrMFTUnavailable
	}
	comCall(sample, 42, buf)
	comRelease(buf)
	comCall(sample, 36, uintptr(sampleTime))
	comCall(sample, 38, uintptr(duration))
	return sample, nil
}

func validWindowsFrameDimensions(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	maxInt := uint64(^uint(0) >> 1)
	return uint64(width) <= maxInt/uint64(height) && uint64(width)*uint64(height) <= maxInt/4
}
