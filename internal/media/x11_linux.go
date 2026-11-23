//go:build linux

package media

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
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
	openDisplay  func(*byte) uintptr
	defaultRoot  func(uintptr) uintptr
	maxWidth     int
	maxHeight    int
	lastHash     uint64
	hasLastHash  bool
	closeLibrary func(uintptr) error
}

var (
	x11ErrorHandlerOnce sync.Once
	// x11ErrorCount counts X protocol errors reported to the installed handler.
	x11ErrorCount atomic.Uint64
)

// installX11ErrorHandler replaces Xlib's default protocol error handler, which
// prints and calls exit(), with one that only records the error. Failed requests
// (e.g. XGetImage BadMatch after a resolution change) then simply return NULL.
// The IO error handler is intentionally left alone: Xlib terminates the process
// after an IO error handler returns, so it cannot be made safe from Go.
func installX11ErrorHandler(lookup func(string) uintptr) {
	x11ErrorHandlerOnce.Do(func() {
		addr := lookup("XSetErrorHandler")
		if addr == 0 {
			return
		}
		var setErrorHandler func(uintptr) uintptr
		purego.RegisterFunc(&setErrorHandler, addr)
		handler := purego.NewCallback(func(display uintptr, event uintptr) uintptr {
			x11ErrorCount.Add(1)
			return 0
		})
		setErrorHandler(handler)
	})
}

func newNativeScreenCapture(opts CaptureOptions) nativeScreenCapture {
	if runtime.GOOS != "linux" {
		return nil
	}
	if os.Getenv("DISPLAY") == "" {
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			return newWaylandCapture(opts)
		}
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
	installX11ErrorHandler(lookup)
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
	return &x11Capture{handle: lib, display: display, root: root, width: width, height: height, closeDisplay: closeDisplay, getGeometry: getGeometry, getImage: getImage, destroyImage: destroyImage, openDisplay: openDisplay, defaultRoot: defaultRoot, maxWidth: opts.Width, maxHeight: opts.Height}
}

func (x *x11Capture) Backend() CaptureBackend { return CaptureBackendX11 }

func (x *x11Capture) Capture(maxW, maxH int, withNV12 bool) (*core.VideoFrame, bool, error) {
	if x == nil {
		return nil, false, errors.New("media: X11 display is closed")
	}
	if x.display == 0 && !x.reopen() {
		return nil, false, errors.New("media: X11 display is closed")
	}
	// Refresh the root geometry each frame so a resolution change does not make
	// XGetImage request an out-of-bounds rectangle (BadMatch).
	if !x.refreshGeometry() && !x.reopen() {
		return nil, false, errors.New("media: X11 root geometry unavailable")
	}
	imagePtr := x.getImage(x.display, x.root, 0, 0, x.width, x.height, x11AllPlanes, x11ZPixmap)
	if imagePtr == 0 && x.reopen() {
		imagePtr = x.getImage(x.display, x.root, 0, 0, x.width, x.height, x11AllPlanes, x11ZPixmap)
	}
	if imagePtr == 0 {
		return nil, false, errors.New("media: XGetImage failed; X11 display reconnect did not produce an image")
	}
	defer x.destroyImage(imagePtr)
	img := (*x11Image)(unsafe.Pointer(imagePtr))
	if img.Data == 0 || img.Width <= 0 || img.Height <= 0 || (img.BitsPerPixel != 32 && img.BitsPerPixel != 24) || img.BytesPerLine <= 0 {
		return nil, false, fmt.Errorf("media: unsupported XImage format bpp=%d depth=%d", img.BitsPerPixel, img.Depth)
	}
	width, height := int(img.Width), int(img.Height)
	data := unsafe.Slice((*byte)(unsafe.Pointer(img.Data)), int(img.BytesPerLine)*height)
	rgba := make([]byte, width*height*4)
	bytesPerPixel := int((img.BitsPerPixel + 7) / 8)
	for row := 0; row < height; row++ {
		for col := 0; col < width; col++ {
			off := row*int(img.BytesPerLine) + col*bytesPerPixel
			pixel := x11ReadPixel(data[off:off+bytesPerPixel], int(img.BitsPerPixel), img.ByteOrder)
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
	frameHash := x11FrameHash(rgba)
	changed := !x.hasLastHash || frameHash != x.lastHash
	x.lastHash, x.hasLastHash = frameHash, true
	return frame, changed, nil
}

func x11ReadPixel(data []byte, bitsPerPixel int, byteOrder int32) uint32 {
	var pixel uint32
	if byteOrder == 0 {
		for i := len(data) - 1; i >= 0; i-- {
			pixel = pixel<<8 | uint32(data[i])
		}
		return pixel
	}
	for _, value := range data {
		pixel = pixel<<8 | uint32(value)
	}
	return pixel
}

func x11FrameHash(data []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return h.Sum64()
}

func x11FrameChanged(data []byte, previous uint64, hasPrevious bool) bool {
	current := x11FrameHash(data)
	return !hasPrevious || current != previous
}

// refreshGeometry re-reads the root window size and applies the configured clamp.
func (x *x11Capture) refreshGeometry() bool {
	if x == nil || x.display == 0 || x.getGeometry == nil {
		return false
	}
	var rootID uintptr
	var px, py int32
	var width, height, border, depth uint32
	if x.getGeometry(x.display, x.root, &rootID, &px, &py, &width, &height, &border, &depth) == 0 || width == 0 || height == 0 {
		return false
	}
	x.width, x.height = x.clampSize(width, height)
	return true
}

func (x *x11Capture) clampSize(width, height uint32) (uint32, uint32) {
	if x.maxWidth > 0 && x.maxWidth < int(width) {
		width = uint32(x.maxWidth)
	}
	if x.maxHeight > 0 && x.maxHeight < int(height) {
		height = uint32(x.maxHeight)
	}
	return width, height
}

func (x *x11Capture) reopen() bool {
	if x == nil || x.openDisplay == nil || x.defaultRoot == nil {
		return false
	}
	if x.display != 0 && x.closeDisplay != nil {
		_ = x.closeDisplay(x.display)
	}
	display := x.openDisplay(nil)
	if display == 0 {
		x.display = 0
		return false
	}
	root := x.defaultRoot(display)
	var rootID uintptr
	var px, py int32
	var width, height, border, depth uint32
	if root == 0 || x.getGeometry(display, root, &rootID, &px, &py, &width, &height, &border, &depth) == 0 || width == 0 || height == 0 {
		_ = x.closeDisplay(display)
		x.display = 0
		return false
	}
	width, height = x.clampSize(width, height)
	x.display, x.root, x.width, x.height = display, root, width, height
	return true
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
	handle := x.handle
	x.handle = 0
	if handle == 0 {
		return nil
	}
	if x.closeLibrary != nil {
		return x.closeLibrary(handle)
	}
	return purego.Dlclose(handle)
}
