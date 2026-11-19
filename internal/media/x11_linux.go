//go:build linux

package media

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"nexlab/internal/core"
)

const (
	x11ZPixmap   = 2
	x11AllPlanes = ^uint64(0)
	rtldNow      = 2
)

type x11Image struct {
	Width          int32
	Height         int32
	XOffset        int32
	Format         int32
	Data           uintptr
	ByteOrder      int32
	BitmapUnit     int32
	BitmapBitOrder int32
	BitmapPad      int32
	Depth          int32
	BytesPerLine   int32
	BitsPerPixel   int32
	RedMask        uintptr
	GreenMask      uintptr
	BlueMask       uintptr
	Obdata         uintptr
	Functions      uintptr
}

type x11Capture struct {
	handle       uintptr
	display      uintptr
	root         uintptr
	width        uint32
	height       uint32
	closeDisplay func(uintptr) int32
	getGeometry  func(uintptr, uintptr, *uintptr, *int32, *int32, *uint32, *uint32, *uint32, *uint32) int32
	getImage     func(uintptr, uintptr, int32, int32, uint32, uint32, uint64, int32) uintptr
	destroyImage func(uintptr) int32
}

func newNativeScreenCapture(opts CaptureOptions) nativeScreenCapture {
	if runtime.GOOS != "linux" || os.Getenv("DISPLAY") == "" {
		return nil
	}
	lib, err := purego.Dlopen("libX11.so.6", rtldNow)
	if err != nil {
		return nil
	}
	lookup := func(name string) uintptr {
		addr, _ := purego.Dlsym(lib, name)
		return addr
	}
	var openDisplay func(*byte) uintptr
	var defaultRoot func(uintptr) uintptr
	var closeDisplay func(uintptr) int32
	var getGeometry func(uintptr, uintptr, *uintptr, *int32, *int32, *uint32, *uint32, *uint32, *uint32) int32
	var getImage func(uintptr, uintptr, int32, int32, uint32, uint32, uint64, int32) uintptr
	var destroyImage func(uintptr) int32
	if lookup("XOpenDisplay") == 0 || lookup("XDefaultRootWindow") == 0 || lookup("XCloseDisplay") == 0 || lookup("XGetGeometry") == 0 || lookup("XGetImage") == 0 || lookup("XDestroyImage") == 0 {
		_ = purego.Dlclose(lib)
		return nil
	}
	purego.RegisterFunc(&openDisplay, lookup("XOpenDisplay"))
	purego.RegisterFunc(&defaultRoot, lookup("XDefaultRootWindow"))
	purego.RegisterFunc(&closeDisplay, lookup("XCloseDisplay"))
	purego.RegisterFunc(&getGeometry, lookup("XGetGeometry"))
	purego.RegisterFunc(&getImage, lookup("XGetImage"))
	purego.RegisterFunc(&destroyImage, lookup("XDestroyImage"))
	display := openDisplay(nil)
	if display == 0 {
		_ = purego.Dlclose(lib)
		return nil
	}
	root := defaultRoot(display)
	var rootID uintptr
	var x, y int32
	var width, height, border, depth uint32
	if getGeometry(display, root, &rootID, &x, &y, &width, &height, &border, &depth) == 0 || width == 0 || height == 0 {
		_ = closeDisplay(display)
		_ = purego.Dlclose(lib)
		return nil
	}
	if opts.Width > 0 && uint32(opts.Width) < width {
		width = uint32(opts.Width)
	}
	if opts.Height > 0 && uint32(opts.Height) < height {
		height = uint32(opts.Height)
	}
	return &x11Capture{handle: lib, display: display, root: root, width: width, height: height, closeDisplay: closeDisplay, getGeometry: getGeometry, getImage: getImage, destroyImage: destroyImage}
}

func (x *x11Capture) Backend() CaptureBackend { return CaptureBackendX11 }

func (x *x11Capture) Capture(maxW, maxH int, withNV12 bool) (*core.VideoFrame, bool, error) {
	if x == nil || x.display == 0 {
		return nil, false, errors.New("media: X11 display is closed")
	}
	imagePtr := x.getImage(x.display, x.root, 0, 0, x.width, x.height, x11AllPlanes, x11ZPixmap)
	if imagePtr == 0 {
		return nil, false, errors.New("media: XGetImage failed")
	}
	defer x.destroyImage(imagePtr)
	img := (*x11Image)(unsafe.Pointer(imagePtr))
	if img.Data == 0 || img.Width <= 0 || img.Height <= 0 || img.BitsPerPixel != 32 || img.BytesPerLine <= 0 {
		return nil, false, fmt.Errorf("media: unsupported XImage format bpp=%d depth=%d", img.BitsPerPixel, img.Depth)
	}
	width, height := int(img.Width), int(img.Height)
	data := unsafe.Slice((*byte)(unsafe.Pointer(img.Data)), int(img.BytesPerLine)*height)
	rgba := make([]byte, width*height*4)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			off := row*int(img.BytesPerLine) + col*4
			pixel := uint32(data[off]) | uint32(data[off+1])<<8 | uint32(data[off+2])<<16 | uint32(data[off+3])<<24
			if img.ByteOrder != 0 {
				pixel = uint32(data[off+3]) | uint32(data[off+2])<<8 | uint32(data[off+1])<<16 | uint32(data[off])<<24
			}
			r, g, b := x11ExtractRGB(uint64(pixel), uint64(img.RedMask), uint64(img.GreenMask), uint64(img.BlueMask))
			out := (row*width + col) * 4
			rgba[out], rgba[out+1], rgba[out+2], rgba[out+3] = r, g, b, 255
		}
	}
	frame := &core.VideoFrame{Seq: uint64(time.Now().UnixNano()), Codec: core.CodecFrameZstd, Width: width, Height: height, KeyFrame: true, Timestamp: time.Now().UnixMilli(), Data: rgba}
	if (maxW > 0 && width > maxW) || (maxH > 0 && height > maxH) {
		frame = DownscaleFrame(frame, maxW, maxH)
	}
	if withNV12 {
		_ = EnsureNV12(frame)
	}
	return frame, true, nil
}

func x11ExtractRGB(pixel, redMask, greenMask, blueMask uint64) (byte, byte, byte) {
	extract := func(mask uint64) byte {
		if mask == 0 {
			return 0
		}
		shift := uint(0)
		for ((mask >> shift) & 1) == 0 {
			shift++
		}
		value := (pixel & mask) >> shift
		bits := uint(0)
		for m := mask >> shift; m > 0; m >>= 1 {
			bits++
		}
		if bits >= 8 {
			return byte(value >> (bits - 8))
		}
		return byte((value * 255) / ((uint64(1) << bits) - 1))
	}
	return extract(redMask), extract(greenMask), extract(blueMask)
}

func (x *x11Capture) Close() error {
	if x == nil {
		return nil
	}
	if x.display != 0 && x.closeDisplay != nil {
		_ = x.closeDisplay(x.display)
		x.display = 0
	}
	if x.handle != 0 {
		err := purego.Dlclose(x.handle)
		x.handle = 0
		return err
	}
	return nil
}
