//go:build windows

package media

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/windows"
	"nexlab/internal/core"
)

var (
	d3d11DLL = windows.NewLazySystemDLL("d3d11.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	procD3D11CreateDevice = d3d11DLL.NewProc("D3D11CreateDevice")

	procGetDesktopWindow = user32.NewProc("GetDesktopWindow")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
)

const (
	d3dDriverTypeHardware        = 1
	d3d11CreateDeviceBgraSupport = 0x20
	d3d11SDKVersion              = 7
	d3d11UsageStaging            = 3
	d3d11CPUAccessRead           = 0x20000
	d3d11MapRead                 = 1
	dxgiFormatB8G8R8A8Unorm      = 87

	dxgiErrorWaitTimeout = 0x887A0027
	dxgiErrorAccessLost  = 0x887A0026

	smCXScreen   = 0
	smCYScreen   = 1
	srccopy      = 0x00CC0020
	captureblt   = 0x40000000
	dibRGBColors = 0
)

var (
	iidIDXGIDevice     = windows.GUID{Data1: 0x54ec77fa, Data2: 0x1377, Data3: 0x44e6, Data4: [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c}}
	iidIDXGIOutput1    = windows.GUID{Data1: 0x00cddea8, Data2: 0x939b, Data3: 0x4b83, Data4: [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc}}
	iidID3D11Texture2D = windows.GUID{Data1: 0x6f15aaf2, Data2: 0xd208, Data3: 0x4e89, Data4: [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c}}
)

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleCount    uint32
	SampleQuality  uint32
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	PData      uintptr
	RowPitch   uint32
	DepthPitch uint32
}

type dxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerX                  int32
	PointerY                  int32
	PointerVisible            int32
	TotalMetadataBufferSize   uint32
	PointerShapeBufferSize    uint32
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	BmiHeader bitmapInfoHeader
	BmiColors [1]uint32
}

type dxgiDuplicator struct {
	device  uintptr
	ctx     uintptr
	dupl    uintptr
	staging uintptr
	width   int
	height  int
}

// ScreenCapturer captures the primary display via DXGI Desktop Duplication,
// falling back automatically to GDI BitBlt and finally synthetic test frames.
type ScreenCapturer struct {
	mu             sync.Mutex
	opts           CaptureOptions
	seq            uint64
	backend        CaptureBackend
	dxgi           *dxgiDuplicator
	dxgiDisabled   bool
	gdiDisabled    bool
	rgbaBuf        []byte
	bgraBuf        []byte
	lastWidth      int
	lastHeight     int
	hasCachedFrame bool
}

func NewScreenCapturer(opts CaptureOptions) *ScreenCapturer {
	if opts.Width <= 0 {
		opts.Width = 1280
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}
	c := &ScreenCapturer{
		opts:    opts,
		backend: CaptureBackendSynthetic,
	}
	if opts.ForceSynthetic {
		c.dxgiDisabled = true
		c.gdiDisabled = true
		return c
	}
	if err := c.initDXGI(); err == nil {
		c.backend = CaptureBackendDXGI
	} else {
		c.backend = CaptureBackendGDI
	}
	return c
}

func (c *ScreenCapturer) Backend() CaptureBackend {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backend
}

func (c *ScreenCapturer) CaptureScreen() (*core.VideoFrame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.seq++
	var w, h int
	var err error

	if !c.dxgiDisabled {
		w, h, err = c.captureDXGI()
		if err == nil && w > 0 && h > 0 {
			c.backend = CaptureBackendDXGI
			c.lastWidth, c.lastHeight = w, h
			c.hasCachedFrame = true
			return c.buildVideoFrame(w, h), nil
		}
		if errors.Is(err, errDXGIAccessLost) {
			c.releaseDXGI()
			if initErr := c.initDXGI(); initErr == nil {
				w, h, err = c.captureDXGI()
				if err == nil && w > 0 && h > 0 {
					c.backend = CaptureBackendDXGI
					c.lastWidth, c.lastHeight = w, h
					c.hasCachedFrame = true
					return c.buildVideoFrame(w, h), nil
				}
			}
		}
		c.releaseDXGI()
		c.dxgiDisabled = true
	}

	if !c.gdiDisabled {
		w, h, err = c.captureGDI()
		if err == nil && w > 0 && h > 0 {
			c.backend = CaptureBackendGDI
			c.lastWidth, c.lastHeight = w, h
			c.hasCachedFrame = true
			return c.buildVideoFrame(w, h), nil
		}
		c.gdiDisabled = true
	}

	c.backend = CaptureBackendSynthetic
	w, h = c.opts.Width, c.opts.Height
	c.rgbaBuf = renderSyntheticRGBA(c.rgbaBuf, w, h, c.seq)
	return c.buildVideoFrame(w, h), nil
}

func (c *ScreenCapturer) buildVideoFrame(w, h int) *core.VideoFrame {
	need := w * h * 4
	out := make([]byte, need)
	copy(out, c.rgbaBuf[:need])
	frame := &core.VideoFrame{
		Seq:       c.seq,
		Codec:     core.CodecFrameZstd,
		Width:     w,
		Height:    h,
		KeyFrame:  c.seq == 1 || c.seq%60 == 0,
		Timestamp: time.Now().UnixMilli(),
		Data:      out,
	}
	_ = EnsureNV12(frame)
	return frame
}

func (c *ScreenCapturer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releaseDXGI()
	return nil
}

var errDXGIAccessLost = errors.New("media: dxgi access lost")

func (c *ScreenCapturer) initDXGI() error {
	if err := d3d11DLL.Load(); err != nil {
		return err
	}
	featureLevels := [3]uint32{0xb000, 0xa100, 0xa000}
	var device, ctx uintptr
	var chosen uint32
	hr, _, _ := procD3D11CreateDevice.Call(
		0,
		d3dDriverTypeHardware,
		0,
		d3d11CreateDeviceBgraSupport,
		uintptr(unsafe.Pointer(&featureLevels[0])),
		uintptr(len(featureLevels)),
		d3d11SDKVersion,
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&chosen)),
		uintptr(unsafe.Pointer(&ctx)),
	)
	if int32(hr) < 0 || device == 0 || ctx == 0 {
		if device != 0 {
			comRelease(device)
		}
		if ctx != 0 {
			comRelease(ctx)
		}
		return fmt.Errorf("media: D3D11CreateDevice failed: 0x%08x", uint32(hr))
	}

	var dxgiDevice uintptr
	if r := comCall(device, 0, uintptr(unsafe.Pointer(&iidIDXGIDevice)), uintptr(unsafe.Pointer(&dxgiDevice))); int32(r) < 0 || dxgiDevice == 0 {
		comRelease(ctx)
		comRelease(device)
		return fmt.Errorf("media: QueryInterface(IDXGIDevice) failed: 0x%08x", uint32(r))
	}

	var adapter uintptr
	r := comCall(dxgiDevice, 7, uintptr(unsafe.Pointer(&adapter)))
	comRelease(dxgiDevice)
	if int32(r) < 0 || adapter == 0 {
		comRelease(ctx)
		comRelease(device)
		return fmt.Errorf("media: IDXGIDevice::GetAdapter failed: 0x%08x", uint32(r))
	}

	var output uintptr
	r = comCall(adapter, 7, 0, uintptr(unsafe.Pointer(&output)))
	comRelease(adapter)
	if int32(r) < 0 || output == 0 {
		comRelease(ctx)
		comRelease(device)
		return fmt.Errorf("media: IDXGIAdapter::EnumOutputs failed: 0x%08x", uint32(r))
	}

	var output1 uintptr
	r = comCall(output, 0, uintptr(unsafe.Pointer(&iidIDXGIOutput1)), uintptr(unsafe.Pointer(&output1)))
	comRelease(output)
	if int32(r) < 0 || output1 == 0 {
		comRelease(ctx)
		comRelease(device)
		return fmt.Errorf("media: QueryInterface(IDXGIOutput1) failed: 0x%08x", uint32(r))
	}

	var dupl uintptr
	r = comCall(output1, 22, device, uintptr(unsafe.Pointer(&dupl)))
	comRelease(output1)
	if int32(r) < 0 || dupl == 0 {
		comRelease(ctx)
		comRelease(device)
		return fmt.Errorf("media: IDXGIOutput1::DuplicateOutput failed: 0x%08x", uint32(r))
	}

	c.dxgi = &dxgiDuplicator{
		device: device,
		ctx:    ctx,
		dupl:   dupl,
	}
	return nil
}

func (c *ScreenCapturer) releaseDXGI() {
	if c.dxgi == nil {
		return
	}
	if c.dxgi.staging != 0 {
		comRelease(c.dxgi.staging)
	}
	if c.dxgi.dupl != 0 {
		comRelease(c.dxgi.dupl)
	}
	if c.dxgi.ctx != 0 {
		comRelease(c.dxgi.ctx)
	}
	if c.dxgi.device != 0 {
		comRelease(c.dxgi.device)
	}
	c.dxgi = nil
}

func (c *ScreenCapturer) captureDXGI() (int, int, error) {
	if c.dxgi == nil {
		if err := c.initDXGI(); err != nil {
			return 0, 0, err
		}
	}
	d := c.dxgi
	var frameInfo dxgiOutduplFrameInfo
	var resource uintptr
	hr := comCall(d.dupl, 8, 50, uintptr(unsafe.Pointer(&frameInfo)), uintptr(unsafe.Pointer(&resource)))
	if uint32(hr) == dxgiErrorWaitTimeout {
		if c.hasCachedFrame && c.lastWidth > 0 && c.lastHeight > 0 {
			return c.lastWidth, c.lastHeight, nil
		}
		return 0, 0, fmt.Errorf("media: dxgi acquire timeout with no cached frame")
	}
	if uint32(hr) == dxgiErrorAccessLost {
		return 0, 0, errDXGIAccessLost
	}
	if int32(hr) < 0 || resource == 0 {
		return 0, 0, fmt.Errorf("media: AcquireNextFrame failed: 0x%08x", uint32(hr))
	}
	defer comCall(d.dupl, 14)
	defer comRelease(resource)

	var tex uintptr
	if r := comCall(resource, 0, uintptr(unsafe.Pointer(&iidID3D11Texture2D)), uintptr(unsafe.Pointer(&tex))); int32(r) < 0 || tex == 0 {
		return 0, 0, fmt.Errorf("media: QueryInterface(ID3D11Texture2D) failed: 0x%08x", uint32(r))
	}
	defer comRelease(tex)

	var desc d3d11Texture2DDesc
	comCall(tex, 10, uintptr(unsafe.Pointer(&desc)))
	w := int(desc.Width)
	h := int(desc.Height)
	if w <= 0 || h <= 0 {
		return 0, 0, ErrInvalidDimension
	}

	if d.staging == 0 || d.width != w || d.height != h {
		if d.staging != 0 {
			comRelease(d.staging)
			d.staging = 0
		}
		stagingDesc := d3d11Texture2DDesc{
			Width:          desc.Width,
			Height:         desc.Height,
			MipLevels:      1,
			ArraySize:      1,
			Format:         dxgiFormatB8G8R8A8Unorm,
			SampleCount:    1,
			SampleQuality:  0,
			Usage:          d3d11UsageStaging,
			BindFlags:      0,
			CPUAccessFlags: d3d11CPUAccessRead,
			MiscFlags:      0,
		}
		var staging uintptr
		if r := comCall(d.device, 5, uintptr(unsafe.Pointer(&stagingDesc)), 0, uintptr(unsafe.Pointer(&staging))); int32(r) < 0 || staging == 0 {
			return 0, 0, fmt.Errorf("media: CreateTexture2D(staging) failed: 0x%08x", uint32(r))
		}
		d.staging = staging
		d.width = w
		d.height = h
	}

	comCall(d.ctx, 47, d.staging, tex)

	var mapped d3d11MappedSubresource
	if r := comCall(d.ctx, 14, d.staging, 0, d3d11MapRead, 0, uintptr(unsafe.Pointer(&mapped))); int32(r) < 0 || mapped.PData == 0 {
		return 0, 0, fmt.Errorf("media: ID3D11DeviceContext::Map failed: 0x%08x", uint32(r))
	}
	defer comCall(d.ctx, 15, d.staging, 0)

	pitch := int(mapped.RowPitch)
	if pitch < w*4 {
		return 0, 0, ErrBufferTooSmall
	}
	srcSlice := unsafe.Slice((*byte)(unsafe.Pointer(mapped.PData)), pitch*h)
	c.rgbaBuf = bgraToRGBA(srcSlice, c.rgbaBuf, w, h, pitch)
	return w, h, nil
}

func (c *ScreenCapturer) captureGDI() (int, int, error) {
	if err := user32.Load(); err != nil {
		return 0, 0, err
	}
	if err := gdi32.Load(); err != nil {
		return 0, 0, err
	}
	cx, _, _ := procGetSystemMetrics.Call(smCXScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCYScreen)
	w := int(int32(cx))
	h := int(int32(cy))
	if w <= 0 || h <= 0 {
		return 0, 0, ErrInvalidDimension
	}

	hwnd, _, _ := procGetDesktopWindow.Call()
	hdcScreen, _, _ := procGetDC.Call(hwnd)
	if hdcScreen == 0 {
		return 0, 0, errors.New("media: GetDC failed")
	}
	defer procReleaseDC.Call(hwnd, hdcScreen)

	hdcMem, _, _ := procCreateCompatibleDC.Call(hdcScreen)
	if hdcMem == 0 {
		return 0, 0, errors.New("media: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(hdcMem)

	hbmp, _, _ := procCreateCompatibleBitmap.Call(hdcScreen, uintptr(w), uintptr(h))
	if hbmp == 0 {
		return 0, 0, errors.New("media: CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(hbmp)

	oldObj, _, _ := procSelectObject.Call(hdcMem, hbmp)
	if oldObj != 0 {
		defer procSelectObject.Call(hdcMem, oldObj)
	}

	ok, _, err := procBitBlt.Call(
		hdcMem,
		0,
		0,
		uintptr(w),
		uintptr(h),
		hdcScreen,
		0,
		0,
		srccopy|captureblt,
	)
	if ok == 0 {
		return 0, 0, fmt.Errorf("media: BitBlt failed: %v", err)
	}

	need := w * h * 4
	if cap(c.bgraBuf) < need {
		c.bgraBuf = make([]byte, need)
	}
	c.bgraBuf = c.bgraBuf[:need]

	var bmi bitmapInfo
	bmi.BmiHeader.BiSize = uint32(unsafe.Sizeof(bmi.BmiHeader))
	bmi.BmiHeader.BiWidth = int32(w)
	bmi.BmiHeader.BiHeight = -int32(h)
	bmi.BmiHeader.BiPlanes = 1
	bmi.BmiHeader.BiBitCount = 32

	lines, _, err := procGetDIBits.Call(
		hdcMem,
		hbmp,
		0,
		uintptr(h),
		uintptr(unsafe.Pointer(&c.bgraBuf[0])),
		uintptr(unsafe.Pointer(&bmi)),
		dibRGBColors,
	)
	if int(lines) == 0 {
		return 0, 0, fmt.Errorf("media: GetDIBits failed: %v", err)
	}

	c.rgbaBuf = bgraToRGBA(c.bgraBuf, c.rgbaBuf, w, h, w*4)
	return w, h, nil
}

func comMethod(obj uintptr, index int) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	return *(*uintptr)(unsafe.Pointer(vtbl + uintptr(index)*unsafe.Sizeof(uintptr(0))))
}

func comCall(obj uintptr, index int, args ...uintptr) uintptr {
	fn := comMethod(obj, index)
	all := make([]uintptr, 0, 1+len(args))
	all = append(all, obj)
	all = append(all, args...)
	r1, _, _ := purego.SyscallN(fn, all...)
	return r1
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, 2)
	}
}
