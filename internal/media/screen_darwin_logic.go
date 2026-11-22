package media

// Platform-independent pieces of the macOS screen capture backend. This file
// intentionally has no build constraint (the "_logic" suffix keeps Go from
// treating it as darwin-only) so the pixel conversion, scaling, version
// parsing, permission and backend state machines are unit-tested on every OS.

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// macOS version detection
// ---------------------------------------------------------------------------

type darwinScreenOSVersion struct {
	Major, Minor, Patch int
}

func (v darwinScreenOSVersion) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

func (v darwinScreenOSVersion) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

func darwinScreenParseVersionParts(s string) ([3]int, int, bool) {
	var out [3]int
	s = strings.TrimSpace(s)
	if s == "" {
		return out, 0, false
	}
	parts := strings.Split(s, ".")
	n := 0
	for i, p := range parts {
		if i >= 3 {
			break
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return out, 0, false
		}
		out[i] = v
		n++
	}
	return out, n, n > 0
}

// darwinScreenParseProductVersion parses kern.osproductversion ("14.4.1").
func darwinScreenParseProductVersion(s string) (darwinScreenOSVersion, bool) {
	p, _, ok := darwinScreenParseVersionParts(s)
	if !ok || p[0] == 0 {
		return darwinScreenOSVersion{}, false
	}
	return darwinScreenOSVersion{Major: p[0], Minor: p[1], Patch: p[2]}, true
}

// darwinScreenVersionFromKernelRelease maps kern.osrelease (Darwin kernel
// version, e.g. "23.4.0") to the macOS major version. Only the major version is
// reliable: Darwin 20-24 => macOS 11-15, Darwin 25+ => macOS 26+ (Apple jumped
// to year-based numbering), Darwin <20 => macOS 10.x.
func darwinScreenVersionFromKernelRelease(s string) (darwinScreenOSVersion, bool) {
	p, _, ok := darwinScreenParseVersionParts(s)
	if !ok || p[0] < 5 {
		return darwinScreenOSVersion{}, false
	}
	d := p[0]
	switch {
	case d >= 25:
		return darwinScreenOSVersion{Major: d + 1}, true
	case d >= 20:
		return darwinScreenOSVersion{Major: d - 9}, true
	default:
		return darwinScreenOSVersion{Major: 10, Minor: d - 4}, true
	}
}

// darwinScreenResolveOSVersion prefers the product version but falls back to
// the kernel version when the product version is missing or is the "10.16"
// compatibility value macOS reports to binaries linked against old SDKs.
func darwinScreenResolveOSVersion(product, kernel string) darwinScreenOSVersion {
	pv, pok := darwinScreenParseProductVersion(product)
	kv, kok := darwinScreenVersionFromKernelRelease(kernel)
	compat := pok && pv.Major == 10 && pv.Minor >= 16
	switch {
	case pok && !compat:
		return pv
	case kok:
		return kv
	case pok:
		return darwinScreenOSVersion{Major: 11}
	}
	return darwinScreenOSVersion{}
}

// ---------------------------------------------------------------------------
// Pixel layout and conversion
// ---------------------------------------------------------------------------

// CGBitmapInfo / CGImageAlphaInfo constants (CoreGraphics/CGImage.h).
const (
	darwinScreenAlphaInfoMask           uint32 = 0x1F
	darwinScreenAlphaNone               uint32 = 0
	darwinScreenAlphaPremultipliedLast  uint32 = 1
	darwinScreenAlphaPremultipliedFirst uint32 = 2
	darwinScreenAlphaLast               uint32 = 3
	darwinScreenAlphaFirst              uint32 = 4
	darwinScreenAlphaNoneSkipLast       uint32 = 5
	darwinScreenAlphaNoneSkipFirst      uint32 = 6
	darwinScreenAlphaOnly               uint32 = 7
	darwinScreenFloatComponents         uint32 = 1 << 8
	darwinScreenByteOrderMask           uint32 = 0x7000
	darwinScreenByteOrderDefault        uint32 = 0
	darwinScreenByteOrder16Little       uint32 = 1 << 12
	darwinScreenByteOrder32Little       uint32 = 2 << 12
	darwinScreenByteOrder16Big          uint32 = 3 << 12
	darwinScreenByteOrder32Big          uint32 = 4 << 12
	darwinScreenPixelFormatMask         uint32 = 0xF0000

	// Bitmap context format we render into when the source layout is unknown or
	// needs resampling: memory order R,G,B,X.
	darwinScreenContextBitmapInfo = darwinScreenAlphaNoneSkipLast | darwinScreenByteOrder32Big
)

// darwinScreenPixelLayout is the in-memory byte order of a 32-bit pixel.
type darwinScreenPixelLayout int

const (
	darwinScreenLayoutUnknown darwinScreenPixelLayout = iota
	darwinScreenLayoutBGRA
	darwinScreenLayoutRGBA
	darwinScreenLayoutARGB
	darwinScreenLayoutABGR
)

func (l darwinScreenPixelLayout) String() string {
	switch l {
	case darwinScreenLayoutBGRA:
		return "BGRA"
	case darwinScreenLayoutRGBA:
		return "RGBA"
	case darwinScreenLayoutARGB:
		return "ARGB"
	case darwinScreenLayoutABGR:
		return "ABGR"
	}
	return "unknown"
}

// darwinScreenLayoutFromBitmapInfo decodes CGImageGetBitmapInfo for 8-bit,
// 32-bpp images. Anything else (float, 16-bit, packed 10-bit formats, alpha-only,
// 24-bpp) is reported as unknown and handled by redrawing through CoreGraphics.
func darwinScreenLayoutFromBitmapInfo(info uint32, bitsPerComponent, bitsPerPixel int) darwinScreenPixelLayout {
	if bitsPerComponent != 8 || bitsPerPixel != 32 {
		return darwinScreenLayoutUnknown
	}
	if info&darwinScreenFloatComponents != 0 || info&darwinScreenPixelFormatMask != 0 {
		return darwinScreenLayoutUnknown
	}
	var alphaFirst bool
	switch info & darwinScreenAlphaInfoMask {
	case darwinScreenAlphaPremultipliedFirst, darwinScreenAlphaFirst, darwinScreenAlphaNoneSkipFirst:
		alphaFirst = true
	case darwinScreenAlphaPremultipliedLast, darwinScreenAlphaLast, darwinScreenAlphaNoneSkipLast:
		alphaFirst = false
	default:
		return darwinScreenLayoutUnknown
	}
	switch info & darwinScreenByteOrderMask {
	case darwinScreenByteOrder32Little:
		// Little-endian 32-bit word: ARGB word => B,G,R,A bytes; RGBA word => A,B,G,R bytes.
		if alphaFirst {
			return darwinScreenLayoutBGRA
		}
		return darwinScreenLayoutABGR
	case darwinScreenByteOrderDefault, darwinScreenByteOrder32Big:
		if alphaFirst {
			return darwinScreenLayoutARGB
		}
		return darwinScreenLayoutRGBA
	}
	return darwinScreenLayoutUnknown
}

var (
	errDarwinScreenBadGeometry = errors.New("media: invalid macOS screen image geometry")
	errDarwinScreenShortBuffer = errors.New("media: macOS screen image buffer is shorter than its geometry")
	errDarwinScreenBadLayout   = errors.New("media: unsupported macOS screen pixel layout")
)

// darwinScreenConvertToRGBA converts a strided 32-bit buffer into tightly packed
// RGBA with opaque alpha. Premultiplied sources are taken as-is: the screen is
// opaque, and for any translucent pixel the stored colour equals the colour
// composited over black, which is what an opaque output should show.
func darwinScreenConvertToRGBA(dst, src []byte, width, height, stride int, layout darwinScreenPixelLayout) ([]byte, error) {
	if width <= 0 || height <= 0 || stride < width*4 {
		return nil, errDarwinScreenBadGeometry
	}
	if len(src) < (height-1)*stride+width*4 {
		return nil, errDarwinScreenShortBuffer
	}
	var ri, gi, bi int
	switch layout {
	case darwinScreenLayoutBGRA:
		ri, gi, bi = 2, 1, 0
	case darwinScreenLayoutRGBA:
		ri, gi, bi = 0, 1, 2
	case darwinScreenLayoutARGB:
		ri, gi, bi = 1, 2, 3
	case darwinScreenLayoutABGR:
		ri, gi, bi = 3, 2, 1
	default:
		return nil, errDarwinScreenBadLayout
	}
	rowBytes := width * 4
	need := rowBytes * height
	if cap(dst) < need {
		dst = make([]byte, need)
	}
	dst = dst[:need]
	for y := 0; y < height; y++ {
		srcRow := src[y*stride : y*stride+rowBytes]
		dstRow := dst[y*rowBytes : (y+1)*rowBytes]
		for x := 0; x < rowBytes; x += 4 {
			p := srcRow[x : x+4 : x+4]
			d := dstRow[x : x+4 : x+4]
			d[0], d[1], d[2], d[3] = p[ri], p[gi], p[bi], 255
		}
	}
	return dst, nil
}

// darwinScreenTargetSize returns the output frame size for a source image: the
// source size when it already fits, otherwise an even-aligned proportional fit
// (matching DownscaleFrame). Output is always clamped to the video limits.
func darwinScreenTargetSize(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return 0, 0
	}
	if maxW <= 0 || maxW > MaxVideoDimension {
		maxW = MaxVideoDimension
	}
	if maxH <= 0 || maxH > MaxVideoDimension {
		maxH = MaxVideoDimension
	}
	if srcW <= maxW && srcH <= maxH && srcW*srcH <= MaxVideoPixels {
		return srcW, srcH
	}
	w, h := fitEvenBounds(srcW, srcH, maxW, maxH)
	for w*h > MaxVideoPixels && w > 2 && h > 2 {
		w, h = fitEvenBounds(srcW, srcH, w*9/10, h*9/10)
	}
	return w, h
}

// darwinScreenNeedsRedraw reports whether a CGImage must be drawn through a
// CoreGraphics bitmap context (unknown layout or resampling) instead of being
// copied directly from its data provider.
func darwinScreenNeedsRedraw(layout darwinScreenPixelLayout, srcW, srcH, dstW, dstH int) bool {
	return layout == darwinScreenLayoutUnknown || srcW != dstW || srcH != dstH
}

// ---------------------------------------------------------------------------
// Permission state machine
// ---------------------------------------------------------------------------

type darwinScreenPermissionState int

const (
	darwinScreenPermissionUnknown darwinScreenPermissionState = iota
	darwinScreenPermissionGranted
	// darwinScreenPermissionRequested: the system prompt was triggered in this
	// process and access is still not granted. macOS usually applies a new grant
	// only after the app is relaunched.
	darwinScreenPermissionRequested
	darwinScreenPermissionDenied
)

func (s darwinScreenPermissionState) String() string {
	switch s {
	case darwinScreenPermissionGranted:
		return "granted"
	case darwinScreenPermissionRequested:
		return "requested"
	case darwinScreenPermissionDenied:
		return "denied"
	}
	return "unknown"
}

const darwinScreenPermissionRecheck = 2 * time.Second

// darwinScreenPermissionTracker decides when to call CGPreflightScreenCaptureAccess
// and when to call CGRequestScreenCaptureAccess (at most once per tracker).
type darwinScreenPermissionTracker struct {
	state     darwinScreenPermissionState
	requested bool
	lastCheck time.Time
	interval  time.Duration
}

// NeedsCheck reports whether the preflight API should be consulted now.
func (t *darwinScreenPermissionTracker) NeedsCheck(now time.Time) bool {
	switch t.state {
	case darwinScreenPermissionGranted:
		return false
	case darwinScreenPermissionUnknown:
		return true
	}
	interval := t.interval
	if interval <= 0 {
		interval = darwinScreenPermissionRecheck
	}
	return now.Sub(t.lastCheck) >= interval
}

// Observe records a preflight result and reports whether the caller should
// trigger the system permission request.
func (t *darwinScreenPermissionTracker) Observe(preflight bool, allowRequest bool, now time.Time) (state darwinScreenPermissionState, shouldRequest bool) {
	t.lastCheck = now
	if preflight {
		t.state = darwinScreenPermissionGranted
		return t.state, false
	}
	if allowRequest && !t.requested {
		t.requested = true
		t.state = darwinScreenPermissionRequested
		return t.state, true
	}
	if t.requested {
		t.state = darwinScreenPermissionRequested
	} else {
		t.state = darwinScreenPermissionDenied
	}
	return t.state, false
}

// Revoke is called when a capture API reports a permission failure even though
// the preflight said access was granted.
func (t *darwinScreenPermissionTracker) Revoke(now time.Time) {
	t.state = darwinScreenPermissionDenied
	t.lastCheck = now
}

func (t *darwinScreenPermissionTracker) State() darwinScreenPermissionState { return t.state }

// ---------------------------------------------------------------------------
// Backend selection state machine
// ---------------------------------------------------------------------------

type darwinScreenBackendKind int

const (
	darwinScreenBackendNone darwinScreenBackendKind = iota
	darwinScreenBackendSCK
	darwinScreenBackendCG
)

const (
	darwinScreenSCKMinMajor       = 14
	darwinScreenSCKMaxFailures    = 3
	darwinScreenCaptureBackendSCK = CaptureBackend("screencapturekit")
	darwinScreenCaptureBackendCG  = CaptureBackend("coregraphics")
)

func (k darwinScreenBackendKind) CaptureBackend() CaptureBackend {
	switch k {
	case darwinScreenBackendSCK:
		return darwinScreenCaptureBackendSCK
	case darwinScreenBackendCG:
		return darwinScreenCaptureBackendCG
	}
	return CaptureBackendUnavailable
}

// darwinScreenErrKind classifies capture failures for the planner.
type darwinScreenErrKind int

const (
	darwinScreenErrNone darwinScreenErrKind = iota
	darwinScreenErrPermission
	darwinScreenErrDisplayGone
	darwinScreenErrTransient
	darwinScreenErrFatal
)

const darwinScreenSCKErrorDomain = "com.apple.ScreenCaptureKit.SCStreamErrorDomain"

// darwinScreenClassifySCKError maps an NSError (domain, code) from
// ScreenCaptureKit to a failure kind. Codes are SCStreamErrorCode values.
func darwinScreenClassifySCKError(domain string, code int) darwinScreenErrKind {
	if domain != darwinScreenSCKErrorDomain {
		return darwinScreenErrTransient
	}
	switch code {
	case -3801: // SCStreamErrorUserDeclined
		return darwinScreenErrPermission
	case -3803: // SCStreamErrorMissingEntitlements
		return darwinScreenErrFatal
	case -3814, -3815: // SCStreamErrorNoDisplayList, SCStreamErrorNoCaptureSource
		return darwinScreenErrDisplayGone
	}
	return darwinScreenErrTransient
}

// darwinScreenPlanner picks ScreenCaptureKit on macOS 14+ and falls back to
// CoreGraphics after repeated ScreenCaptureKit failures or when it is absent.
type darwinScreenPlanner struct {
	sckSupported bool
	cgSupported  bool
	sckDisabled  bool
	sckFailures  int
}

func newDarwinScreenPlanner(version darwinScreenOSVersion, sckSymbols, cgSymbols bool) *darwinScreenPlanner {
	return &darwinScreenPlanner{
		sckSupported: sckSymbols && version.Major >= darwinScreenSCKMinMajor,
		cgSupported:  cgSymbols,
	}
}

func (p *darwinScreenPlanner) Backend() darwinScreenBackendKind {
	switch {
	case p.sckSupported && !p.sckDisabled:
		return darwinScreenBackendSCK
	case p.cgSupported:
		return darwinScreenBackendCG
	case p.sckSupported:
		return darwinScreenBackendSCK
	}
	return darwinScreenBackendNone
}

func (p *darwinScreenPlanner) ReportSuccess(kind darwinScreenBackendKind) {
	if kind == darwinScreenBackendSCK {
		p.sckFailures = 0
	}
}

// ReportFailure records a failure and reports whether the active backend changed.
func (p *darwinScreenPlanner) ReportFailure(kind darwinScreenBackendKind, errKind darwinScreenErrKind) bool {
	if kind != darwinScreenBackendSCK || errKind == darwinScreenErrPermission {
		// Permission problems affect every backend equally; don't switch.
		return false
	}
	before := p.Backend()
	p.sckFailures++
	if errKind == darwinScreenErrFatal || p.sckFailures >= darwinScreenSCKMaxFailures {
		p.sckDisabled = true
	}
	return p.Backend() != before
}

// ResetForDisplayChange gives ScreenCaptureKit another chance after the main
// display or its resolution changed.
func (p *darwinScreenPlanner) ResetForDisplayChange() {
	p.sckDisabled = false
	p.sckFailures = 0
}

// darwinScreenDisplayTracker detects main display and resolution changes.
type darwinScreenDisplayTracker struct {
	id     uint32
	width  int
	height int
	valid  bool
}

// Update returns displayChanged (different display ID) and sizeChanged.
func (d *darwinScreenDisplayTracker) Update(id uint32, width, height int) (displayChanged, sizeChanged bool) {
	if !d.valid {
		d.id, d.width, d.height, d.valid = id, width, height, true
		return false, false
	}
	displayChanged = d.id != id
	sizeChanged = d.width != width || d.height != height
	d.id, d.width, d.height = id, width, height
	return displayChanged, sizeChanged
}
