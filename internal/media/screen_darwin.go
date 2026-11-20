//go:build darwin

package media

import (
	"errors"
	"fmt"
	"hash/maphash"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"nexlab/internal/core"
)

const (
	darwinScreenCoreGraphicsPath     = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	darwinScreenCoreFoundationPath   = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	darwinScreenFoundationPath       = "/System/Library/Frameworks/Foundation.framework/Foundation"
	darwinScreenCaptureKitPath       = "/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit"
	darwinScreenObjCPath             = "/usr/lib/libobjc.A.dylib"
	darwinScreenInterpolationNone    = 1
	darwinScreenInterpolationMedium  = 4
	darwinScreenPixelFormat32BGRA    = 0x42475241 // 'BGRA' (kCVPixelFormatType_32BGRA)
	darwinScreenSCKCaptureTimeout    = 3 * time.Second
	darwinScreenSCKContentTimeout    = 5 * time.Second
	darwinScreenSCKContentRetryDelay = time.Second
)

// darwinScreenCGRect mirrors CGRect (CGFloat is double on every 64-bit macOS).
type darwinScreenCGRect struct {
	X, Y, W, H float64
}

// darwinScreenAPI holds the dynamically resolved CoreGraphics/CoreFoundation/
// libobjc entry points. Resolved once per process.
type darwinScreenAPI struct {
	err     error
	version darwinScreenOSVersion

	preflight          func() bool
	request            func() bool
	mainDisplayID      func() uint32
	displayCopyMode    func(uint32) uintptr
	modePixelWidth     func(uintptr) uint
	modePixelHeight    func(uintptr) uint
	modeRelease        func(uintptr)
	displayPixelsWide  func(uint32) uint
	displayPixelsHigh  func(uint32) uint
	displayCreateImage func(uint32) uintptr // nil when the symbol is absent

	imageGetWidth            func(uintptr) uint
	imageGetHeight           func(uintptr) uint
	imageGetBitsPerComponent func(uintptr) uint
	imageGetBitsPerPixel     func(uintptr) uint
	imageGetBytesPerRow      func(uintptr) uint
	imageGetBitmapInfo       func(uintptr) uint32
	imageGetDataProvider     func(uintptr) uintptr
	dataProviderCopyData     func(uintptr) uintptr
	colorSpaceCreateRGB      func() uintptr
	colorSpaceRelease        func(uintptr)
	bitmapContextCreate      func(data uintptr, width, height, bitsPerComponent, bytesPerRow uint, space uintptr, info uint32) uintptr
	bitmapContextGetData     func(uintptr) uintptr
	bitmapContextBytesPerRow func(uintptr) uint
	contextSetInterpolation  func(uintptr, int32)
	contextDrawImage         func(uintptr, darwinScreenCGRect, uintptr)
	contextRelease           func(uintptr)

	cfRetain        func(uintptr) uintptr
	cfRelease       func(uintptr)
	cfDataBytePtr   func(uintptr) uintptr
	cfDataGetLength func(uintptr) int

	poolPush func() uintptr
	poolPop  func(uintptr)

	sckAvailable bool
	sel          darwinScreenSelectors
}

type darwinScreenSelectors struct {
	retain, release, alloc, new_, count, objectAtIndex, array            objc.SEL
	displays, displayID, initWithDisplayExcludingWindows                 objc.SEL
	setWidth, setHeight, setShowsCursor, setPixelFormat                  objc.SEL
	getShareableContent, captureImage, code, domain, localizedDesc, utf8 objc.SEL
}

var (
	darwinScreenAPIOnce sync.Once
	darwinScreenAPIInst *darwinScreenAPI
)

func darwinScreenLoadAPI() *darwinScreenAPI {
	darwinScreenAPIOnce.Do(func() {
		darwinScreenAPIInst = darwinScreenResolveAPI()
	})
	return darwinScreenAPIInst
}

func darwinScreenResolveAPI() (api *darwinScreenAPI) {
	api = &darwinScreenAPI{}
	var cg, cf, objcLib uintptr
	keepLibraries := false
	defer func() {
		if !keepLibraries {
			if objcLib != 0 {
				_ = purego.Dlclose(objcLib)
			}
			if cf != 0 {
				_ = purego.Dlclose(cf)
			}
			if cg != 0 {
				_ = purego.Dlclose(cg)
			}
		}
	}()
	defer func() {
		// purego.RegisterFunc panics on unsupported signatures; never let that
		// escape into the capture path.
		if r := recover(); r != nil {
			api = &darwinScreenAPI{err: fmt.Errorf("%w: native binding failed: %v", ErrDarwinScreenCaptureUnavailable, r)}
		}
	}()
	product, _ := syscall.Sysctl("kern.osproductversion")
	kernel, _ := syscall.Sysctl("kern.osrelease")
	api.version = darwinScreenResolveOSVersion(product, kernel)

	var err error
	cg, err = purego.Dlopen(darwinScreenCoreGraphicsPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		api.err = fmt.Errorf("%w: load CoreGraphics: %v", ErrDarwinScreenCaptureUnavailable, err)
		return api
	}
	cf, err = purego.Dlopen(darwinScreenCoreFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		api.err = fmt.Errorf("%w: load CoreFoundation: %v", ErrDarwinScreenCaptureUnavailable, err)
		return api
	}
	objcLib, err = purego.Dlopen(darwinScreenObjCPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		api.err = fmt.Errorf("%w: load libobjc: %v", ErrDarwinScreenCaptureUnavailable, err)
		return api
	}

	var missing []string
	bind := func(lib uintptr, fptr any, name string, required bool) bool {
		addr, err := purego.Dlsym(lib, name)
		if err != nil || addr == 0 {
			if required {
				missing = append(missing, name)
			}
			return false
		}
		purego.RegisterFunc(fptr, addr)
		return true
	}
	bind(cg, &api.preflight, "CGPreflightScreenCaptureAccess", true)
	bind(cg, &api.request, "CGRequestScreenCaptureAccess", true)
	bind(cg, &api.mainDisplayID, "CGMainDisplayID", true)
	bind(cg, &api.displayCopyMode, "CGDisplayCopyDisplayMode", true)
	bind(cg, &api.modePixelWidth, "CGDisplayModeGetPixelWidth", true)
	bind(cg, &api.modePixelHeight, "CGDisplayModeGetPixelHeight", true)
	bind(cg, &api.modeRelease, "CGDisplayModeRelease", true)
	bind(cg, &api.displayPixelsWide, "CGDisplayPixelsWide", true)
	bind(cg, &api.displayPixelsHigh, "CGDisplayPixelsHigh", true)
	// Deprecated in macOS 14 and hidden from the macOS 15 SDK; optional.
	if !bind(cg, &api.displayCreateImage, "CGDisplayCreateImage", false) {
		api.displayCreateImage = nil
	}
	bind(cg, &api.imageGetWidth, "CGImageGetWidth", true)
	bind(cg, &api.imageGetHeight, "CGImageGetHeight", true)
	bind(cg, &api.imageGetBitsPerComponent, "CGImageGetBitsPerComponent", true)
	bind(cg, &api.imageGetBitsPerPixel, "CGImageGetBitsPerPixel", true)
	bind(cg, &api.imageGetBytesPerRow, "CGImageGetBytesPerRow", true)
	bind(cg, &api.imageGetBitmapInfo, "CGImageGetBitmapInfo", true)
	bind(cg, &api.imageGetDataProvider, "CGImageGetDataProvider", true)
	bind(cg, &api.dataProviderCopyData, "CGDataProviderCopyData", true)
	bind(cg, &api.colorSpaceCreateRGB, "CGColorSpaceCreateDeviceRGB", true)
	bind(cg, &api.colorSpaceRelease, "CGColorSpaceRelease", true)
	bind(cg, &api.bitmapContextCreate, "CGBitmapContextCreate", true)
	bind(cg, &api.bitmapContextGetData, "CGBitmapContextGetData", true)
	bind(cg, &api.bitmapContextBytesPerRow, "CGBitmapContextGetBytesPerRow", true)
	bind(cg, &api.contextSetInterpolation, "CGContextSetInterpolationQuality", true)
	bind(cg, &api.contextDrawImage, "CGContextDrawImage", true)
	bind(cg, &api.contextRelease, "CGContextRelease", true)
	bind(cf, &api.cfRetain, "CFRetain", true)
	bind(cf, &api.cfRelease, "CFRelease", true)
	bind(cf, &api.cfDataBytePtr, "CFDataGetBytePtr", true)
	bind(cf, &api.cfDataGetLength, "CFDataGetLength", true)
	bind(objcLib, &api.poolPush, "objc_autoreleasePoolPush", true)
	bind(objcLib, &api.poolPop, "objc_autoreleasePoolPop", true)
	if len(missing) > 0 {
		api.err = fmt.Errorf("%w: missing symbols %v (macOS %s)", ErrDarwinScreenCaptureUnavailable, missing, api.version)
		return api
	}

	if api.version.Major >= darwinScreenSCKMinMajor {
		if _, err := purego.Dlopen(darwinScreenFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			if _, err := purego.Dlopen(darwinScreenCaptureKitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
				api.sckAvailable = objc.GetClass("SCScreenshotManager") != 0 &&
					objc.GetClass("SCShareableContent") != 0 &&
					objc.GetClass("SCContentFilter") != 0 &&
					objc.GetClass("SCStreamConfiguration") != 0 &&
					objc.GetClass("NSArray") != 0
			}
		}
		if api.sckAvailable {
			api.sel = darwinScreenSelectors{
				retain:                          objc.RegisterName("retain"),
				release:                         objc.RegisterName("release"),
				alloc:                           objc.RegisterName("alloc"),
				new_:                            objc.RegisterName("new"),
				count:                           objc.RegisterName("count"),
				objectAtIndex:                   objc.RegisterName("objectAtIndex:"),
				array:                           objc.RegisterName("array"),
				displays:                        objc.RegisterName("displays"),
				displayID:                       objc.RegisterName("displayID"),
				initWithDisplayExcludingWindows: objc.RegisterName("initWithDisplay:excludingWindows:"),
				setWidth:                        objc.RegisterName("setWidth:"),
				setHeight:                       objc.RegisterName("setHeight:"),
				setShowsCursor:                  objc.RegisterName("setShowsCursor:"),
				setPixelFormat:                  objc.RegisterName("setPixelFormat:"),
				getShareableContent:             objc.RegisterName("getShareableContentWithCompletionHandler:"),
				captureImage:                    objc.RegisterName("captureImageWithFilter:configuration:completionHandler:"),
				code:                            objc.RegisterName("code"),
				domain:                          objc.RegisterName("domain"),
				localizedDesc:                   objc.RegisterName("localizedDescription"),
				utf8:                            objc.RegisterName("UTF8String"),
			}
		}
	}
	if !api.sckAvailable && api.displayCreateImage == nil {
		api.err = fmt.Errorf("%w: neither ScreenCaptureKit screenshots nor CGDisplayCreateImage are available (macOS %s)", ErrDarwinScreenCaptureUnavailable, api.version)
	}
	if api.err == nil {
		keepLibraries = true
	}
	return api
}

// withPool runs fn on a locked OS thread inside an autorelease pool so that
// autoreleased Objective-C objects are drained every frame instead of piling up
// on long-lived Go worker threads.
func (api *darwinScreenAPI) withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := api.poolPush()
	defer api.poolPop(pool)
	fn()
}

func (api *darwinScreenAPI) release(ref uintptr) {
	if ref != 0 {
		api.cfRelease(ref)
	}
}

func (api *darwinScreenAPI) displayPixelSize(id uint32) (int, int) {
	if mode := api.displayCopyMode(id); mode != 0 {
		w, h := int(api.modePixelWidth(mode)), int(api.modePixelHeight(mode))
		api.modeRelease(mode)
		if w > 0 && h > 0 {
			return w, h
		}
	}
	return int(api.displayPixelsWide(id)), int(api.displayPixelsHigh(id))
}

// ---------------------------------------------------------------------------
// Permission
// ---------------------------------------------------------------------------

var darwinScreenRequestOnce sync.Once

func darwinScreenPermissionError(state darwinScreenPermissionState) error {
	hint := "enable it in System Settings > Privacy & Security > Screen Recording"
	if state == darwinScreenPermissionRequested {
		hint = "the system prompt was shown; enable it in System Settings > Privacy & Security > Screen Recording and relaunch the app"
	}
	return fmt.Errorf("%w (%s, state=%s): %w", ErrDarwinScreenCapturePermission, hint, state, ErrDarwinScreenCaptureUnavailable)
}

// DarwinScreenCapturePermissionGranted reports, without prompting, whether the
// process currently holds the macOS Screen Recording permission.
func DarwinScreenCapturePermissionGranted() bool {
	api := darwinScreenLoadAPI()
	if api.err != nil || api.preflight == nil {
		return false
	}
	return api.preflight()
}

// RequestDarwinScreenCaptureAccess triggers the macOS Screen Recording prompt
// (at most once per process) and reports whether access is granted now. A grant
// made in System Settings normally takes effect only after the app relaunches.
func RequestDarwinScreenCaptureAccess() bool {
	api := darwinScreenLoadAPI()
	if api.err != nil || api.preflight == nil {
		return false
	}
	if api.preflight() {
		return true
	}
	granted := false
	darwinScreenRequestOnce.Do(func() { granted = api.request() })
	return granted || api.preflight()
}

// DarwinScreenCaptureProbe reports the backend that would be used and, when
// capture is not possible, why (wrapping ErrDarwinScreenCaptureUnavailable and,
// for missing access, ErrDarwinScreenCapturePermission). It never prompts.
func DarwinScreenCaptureProbe() (CaptureBackend, error) {
	api := darwinScreenLoadAPI()
	if api.err != nil {
		return CaptureBackendUnavailable, api.err
	}
	backend := newDarwinScreenPlanner(api.version, api.sckAvailable, api.displayCreateImage != nil).Backend().CaptureBackend()
	if !api.preflight() {
		return CaptureBackendUnavailable, darwinScreenPermissionError(darwinScreenPermissionDenied)
	}
	return backend, nil
}

// ---------------------------------------------------------------------------
// Async completion handling for ScreenCaptureKit blocks
// ---------------------------------------------------------------------------

type darwinScreenAsyncResult struct {
	obj    uintptr
	hasErr bool
	domain string
	code   int
	desc   string
}

// darwinScreenAsync bridges a one-shot completion handler to a waiting Go
// goroutine. If the waiter times out first, a late callback does not retain
// its object, so nothing leaks.
type darwinScreenAsync struct {
	mu        sync.Mutex
	done      chan struct{}
	fired     bool
	abandoned bool
	result    darwinScreenAsyncResult
}

func newDarwinScreenAsync() *darwinScreenAsync {
	return &darwinScreenAsync{done: make(chan struct{})}
}

// complete runs on a ScreenCaptureKit dispatch queue thread.
func (a *darwinScreenAsync) complete(api *darwinScreenAPI, obj uintptr, nserr objc.ID, retain func(uintptr)) {
	defer func() { _ = recover() }()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fired || a.abandoned {
		return
	}
	a.fired = true
	if obj != 0 {
		retain(obj)
		a.result.obj = obj
	}
	if nserr != 0 {
		a.result.hasErr = true
		a.result.code = objc.Send[int](nserr, api.sel.code)
		a.result.domain = darwinScreenNSString(api, objc.Send[objc.ID](nserr, api.sel.domain))
		a.result.desc = darwinScreenNSString(api, objc.Send[objc.ID](nserr, api.sel.localizedDesc))
	}
	close(a.done)
}

func (a *darwinScreenAsync) wait(timeout time.Duration) (darwinScreenAsyncResult, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-a.done:
	case <-timer.C:
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.fired {
		a.abandoned = true
		return darwinScreenAsyncResult{}, false
	}
	return a.result, true
}

func darwinScreenNSString(api *darwinScreenAPI, s objc.ID) string {
	if s == 0 {
		return ""
	}
	return objc.Send[string](s, api.sel.utf8)
}

// ---------------------------------------------------------------------------
// ScreenCaptureKit (macOS 14+) via SCScreenshotManager
// ---------------------------------------------------------------------------

type darwinScreenSCK struct {
	api           *darwinScreenAPI
	filter        objc.ID // retained SCContentFilter
	filterDisplay uint32
	config        objc.ID // retained SCStreamConfiguration
	cfgW, cfgH    int
	lastFetchFail time.Time
}

type darwinScreenSCKError struct {
	kind darwinScreenErrKind
	err  error
}

func (e *darwinScreenSCKError) Error() string { return e.err.Error() }
func (e *darwinScreenSCKError) Unwrap() error { return e.err }

func (s *darwinScreenSCK) fail(kind darwinScreenErrKind, format string, args ...any) error {
	return &darwinScreenSCKError{kind: kind, err: fmt.Errorf("%w: ScreenCaptureKit: %s", ErrDarwinScreenCaptureUnavailable, fmt.Sprintf(format, args...))}
}

func (s *darwinScreenSCK) resultError(op string, r darwinScreenAsyncResult) error {
	kind := darwinScreenClassifySCKError(r.domain, r.code)
	return s.fail(kind, "%s failed: %s (%s %d)", op, r.desc, r.domain, r.code)
}

func (s *darwinScreenSCK) invalidateFilter() {
	if s.filter != 0 {
		s.filter.Send(s.api.sel.release)
		s.filter = 0
	}
	s.filterDisplay = 0
}

func (s *darwinScreenSCK) release() {
	s.invalidateFilter()
	if s.config != 0 {
		s.config.Send(s.api.sel.release)
		s.config = 0
	}
	s.cfgW, s.cfgH = 0, 0
}

func (s *darwinScreenSCK) ensureFilter(displayID uint32) error {
	if s.filter != 0 && s.filterDisplay == displayID {
		return nil
	}
	s.invalidateFilter()
	if !s.lastFetchFail.IsZero() && time.Since(s.lastFetchFail) < darwinScreenSCKContentRetryDelay {
		return s.fail(darwinScreenErrTransient, "shareable content unavailable (retrying shortly)")
	}
	api := s.api
	async := newDarwinScreenAsync()
	block := objc.NewBlock(func(_ objc.Block, content objc.ID, nserr objc.ID) {
		async.complete(api, uintptr(content), nserr, func(obj uintptr) { objc.ID(obj).Send(api.sel.retain) })
	})
	objc.ID(objc.GetClass("SCShareableContent")).Send(api.sel.getShareableContent, block)
	block.Release()
	res, ok := async.wait(darwinScreenSCKContentTimeout)
	if !ok {
		s.lastFetchFail = time.Now()
		return s.fail(darwinScreenErrTransient, "timed out fetching shareable content")
	}
	content := objc.ID(res.obj)
	if content == 0 {
		s.lastFetchFail = time.Now()
		if res.hasErr {
			return s.resultError("fetching shareable content", res)
		}
		return s.fail(darwinScreenErrTransient, "no shareable content returned")
	}
	defer content.Send(api.sel.release)

	displays := objc.Send[objc.ID](content, api.sel.displays)
	var chosen objc.ID
	if displays != 0 {
		n := objc.Send[uint](displays, api.sel.count)
		for i := uint(0); i < n; i++ {
			d := objc.Send[objc.ID](displays, api.sel.objectAtIndex, i)
			if d == 0 {
				continue
			}
			if chosen == 0 {
				chosen = d // fall back to the first display
			}
			if objc.Send[uint32](d, api.sel.displayID) == displayID {
				chosen = d
				break
			}
		}
	}
	if chosen == 0 {
		s.lastFetchFail = time.Now()
		return s.fail(darwinScreenErrDisplayGone, "no displays available")
	}
	emptyWindows := objc.ID(objc.GetClass("NSArray")).Send(api.sel.array)
	filter := objc.ID(objc.GetClass("SCContentFilter")).Send(api.sel.alloc).Send(api.sel.initWithDisplayExcludingWindows, chosen, emptyWindows)
	if filter == 0 {
		s.lastFetchFail = time.Now()
		return s.fail(darwinScreenErrTransient, "SCContentFilter init failed")
	}
	s.filter, s.filterDisplay = filter, displayID
	s.lastFetchFail = time.Time{}
	return nil
}

func (s *darwinScreenSCK) ensureConfig(width, height int) error {
	api := s.api
	if s.config == 0 {
		s.config = objc.ID(objc.GetClass("SCStreamConfiguration")).Send(api.sel.new_)
		if s.config == 0 {
			return s.fail(darwinScreenErrTransient, "SCStreamConfiguration init failed")
		}
		s.config.Send(api.sel.setShowsCursor, true)
		s.config.Send(api.sel.setPixelFormat, uint32(darwinScreenPixelFormat32BGRA))
		s.cfgW, s.cfgH = 0, 0
	}
	if width != s.cfgW || height != s.cfgH {
		s.config.Send(api.sel.setWidth, uint(width))
		s.config.Send(api.sel.setHeight, uint(height))
		s.cfgW, s.cfgH = width, height
	}
	return nil
}

// capture returns a retained CGImageRef the caller must CFRelease.
func (s *darwinScreenSCK) capture(displayID uint32, width, height int) (uintptr, error) {
	if err := s.ensureFilter(displayID); err != nil {
		return 0, err
	}
	if err := s.ensureConfig(width, height); err != nil {
		return 0, err
	}
	api := s.api
	async := newDarwinScreenAsync()
	block := objc.NewBlock(func(_ objc.Block, image uintptr, nserr objc.ID) {
		async.complete(api, image, nserr, func(obj uintptr) { api.cfRetain(obj) })
	})
	objc.ID(objc.GetClass("SCScreenshotManager")).Send(api.sel.captureImage, s.filter, s.config, block)
	block.Release()
	res, ok := async.wait(darwinScreenSCKCaptureTimeout)
	if !ok {
		return 0, s.fail(darwinScreenErrTransient, "timed out capturing screenshot")
	}
	if res.obj == 0 {
		var err error
		if res.hasErr {
			err = s.resultError("captureImage", res)
		} else {
			err = s.fail(darwinScreenErrTransient, "captureImage returned no image")
		}
		var se *darwinScreenSCKError
		if errors.As(err, &se) && se.kind == darwinScreenErrDisplayGone {
			s.invalidateFilter()
		}
		return 0, err
	}
	return res.obj, nil
}

// ---------------------------------------------------------------------------
// nativeScreenCapture implementation
// ---------------------------------------------------------------------------

type darwinScreenCapture struct {
	mu          sync.Mutex
	api         *darwinScreenAPI
	planner     *darwinScreenPlanner
	permission  darwinScreenPermissionTracker
	display     darwinScreenDisplayTracker
	sck         darwinScreenSCK
	backend     CaptureBackend
	closed      bool
	seed        maphash.Seed
	lastHash    uint64
	hasLastHash bool
}

func newDarwinScreenCapture(CaptureOptions) nativeScreenCapture {
	api := darwinScreenLoadAPI()
	if api == nil || api.err != nil {
		return darwinScreenCaptureUnavailable{err: api.errOrDefault()}
	}
	c := &darwinScreenCapture{
		api:     api,
		planner: newDarwinScreenPlanner(api.version, api.sckAvailable, api.displayCreateImage != nil),
		seed:    maphash.MakeSeed(),
	}
	c.sck.api = api
	// Report "unavailable" up front when access is missing so callers do not
	// advertise a working screen share; Capture keeps re-checking and starts
	// producing frames once access is granted.
	c.backend = CaptureBackendUnavailable
	if c.checkPermission(true) == nil {
		c.backend = c.planner.Backend().CaptureBackend()
	}
	return c
}

func (api *darwinScreenAPI) errOrDefault() error {
	if api == nil || api.err == nil {
		return ErrDarwinScreenCaptureUnavailable
	}
	return api.err
}

func (c *darwinScreenCapture) Backend() CaptureBackend {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.backend
}

func (c *darwinScreenCapture) checkPermission(allowRequest bool) error {
	now := time.Now()
	if c.permission.NeedsCheck(now) {
		state, shouldRequest := c.permission.Observe(c.api.preflight(), allowRequest, now)
		if shouldRequest {
			darwinScreenRequestOnce.Do(func() { _ = c.api.request() })
			state, _ = c.permission.Observe(c.api.preflight(), false, now)
		}
		_ = state
	}
	if state := c.permission.State(); state != darwinScreenPermissionGranted {
		return darwinScreenPermissionError(state)
	}
	return nil
}

func (c *darwinScreenCapture) Capture(maxW, maxH int, withNV12 bool) (frame *core.VideoFrame, changed bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, fmt.Errorf("%w: capture is closed", ErrDarwinScreenCaptureUnavailable)
	}
	defer func() {
		if r := recover(); r != nil {
			frame, changed, err = nil, false, fmt.Errorf("%w: native capture panic: %v", ErrDarwinScreenCaptureUnavailable, r)
		}
	}()
	if err := c.checkPermission(true); err != nil {
		return nil, false, err
	}
	var rgba []byte
	var w, h int
	c.api.withPool(func() {
		rgba, w, h, err = c.captureLocked(maxW, maxH)
	})
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	frame = &core.VideoFrame{Seq: uint64(now.UnixNano()), Codec: core.CodecFrameZstd, Width: w, Height: h, KeyFrame: true, Timestamp: now.UnixMilli(), Data: rgba}
	if withNV12 {
		_ = EnsureNV12(frame)
	}
	sum := maphash.Bytes(c.seed, rgba)
	changed = !c.hasLastHash || sum != c.lastHash
	c.lastHash, c.hasLastHash = sum, true
	return frame, changed, nil
}

// captureLocked must run inside withPool.
func (c *darwinScreenCapture) captureLocked(maxW, maxH int) ([]byte, int, int, error) {
	api := c.api
	displayID := api.mainDisplayID()
	if displayID == 0 {
		return nil, 0, 0, fmt.Errorf("%w: no main display", ErrDarwinScreenCaptureUnavailable)
	}
	pw, ph := api.displayPixelSize(displayID)
	if pw <= 0 || ph <= 0 {
		return nil, 0, 0, fmt.Errorf("%w: display %d has no usable mode", ErrDarwinScreenCaptureUnavailable, displayID)
	}
	if displayChanged, sizeChanged := c.display.Update(displayID, pw, ph); displayChanged || sizeChanged {
		if displayChanged {
			c.sck.invalidateFilter()
		}
		c.planner.ResetForDisplayChange()
	}
	tw, th := darwinScreenTargetSize(pw, ph, maxW, maxH)

	var image uintptr
	var lastErr error
	for attempt := 0; attempt < 2 && image == 0; attempt++ {
		kind := c.planner.Backend()
		switch kind {
		case darwinScreenBackendSCK:
			img, err := c.sck.capture(displayID, tw, th)
			if err == nil {
				c.planner.ReportSuccess(kind)
				image = img
				break
			}
			lastErr = err
			errKind := darwinScreenErrTransient
			var se *darwinScreenSCKError
			if errors.As(err, &se) {
				errKind = se.kind
			}
			if errKind == darwinScreenErrPermission {
				c.permission.Revoke(time.Now())
				return nil, 0, 0, darwinScreenPermissionError(darwinScreenPermissionDenied)
			}
			if !c.planner.ReportFailure(kind, errKind) {
				return nil, 0, 0, err
			}
			c.sck.release()
		case darwinScreenBackendCG:
			img := api.displayCreateImage(displayID)
			if img == 0 {
				if !api.preflight() {
					c.permission.Revoke(time.Now())
					return nil, 0, 0, darwinScreenPermissionError(darwinScreenPermissionDenied)
				}
				return nil, 0, 0, fmt.Errorf("%w: CGDisplayCreateImage returned NULL for display %d", ErrDarwinScreenCaptureUnavailable, displayID)
			}
			image = img
		default:
			if lastErr != nil {
				return nil, 0, 0, lastErr
			}
			return nil, 0, 0, fmt.Errorf("%w: no capture backend for macOS %s", ErrDarwinScreenCaptureUnavailable, api.version)
		}
		if image != 0 {
			c.backend = kind.CaptureBackend()
		}
	}
	if image == 0 {
		if lastErr == nil {
			lastErr = fmt.Errorf("%w: no image captured", ErrDarwinScreenCaptureUnavailable)
		}
		return nil, 0, 0, lastErr
	}
	defer api.release(image)
	return c.imageToRGBA(image, maxW, maxH)
}

func (c *darwinScreenCapture) imageToRGBA(image uintptr, maxW, maxH int) ([]byte, int, int, error) {
	api := c.api
	w, h := int(api.imageGetWidth(image)), int(api.imageGetHeight(image))
	if w <= 0 || h <= 0 {
		return nil, 0, 0, fmt.Errorf("%w: empty screen image", ErrDarwinScreenCaptureUnavailable)
	}
	layout := darwinScreenLayoutFromBitmapInfo(api.imageGetBitmapInfo(image), int(api.imageGetBitsPerComponent(image)), int(api.imageGetBitsPerPixel(image)))
	tw, th := darwinScreenTargetSize(w, h, maxW, maxH)
	if !darwinScreenNeedsRedraw(layout, w, h, tw, th) {
		if out, err := c.copyImageData(image, w, h, layout); err == nil {
			return out, w, h, nil
		}
		// fall through to the redraw path on any provider problem
	}
	out, err := c.redrawImage(image, w, h, tw, th)
	if err != nil {
		return nil, 0, 0, err
	}
	return out, tw, th, nil
}

func (c *darwinScreenCapture) copyImageData(image uintptr, w, h int, layout darwinScreenPixelLayout) ([]byte, error) {
	api := c.api
	provider := api.imageGetDataProvider(image)
	if provider == 0 {
		return nil, errDarwinScreenShortBuffer
	}
	data := api.dataProviderCopyData(provider)
	if data == 0 {
		return nil, errDarwinScreenShortBuffer
	}
	defer api.release(data)
	ptr, n := api.cfDataBytePtr(data), api.cfDataGetLength(data)
	if ptr == 0 || n <= 0 {
		return nil, errDarwinScreenShortBuffer
	}
	src := darwinScreenBytes(ptr, n)
	return darwinScreenConvertToRGBA(nil, src, w, h, int(api.imageGetBytesPerRow(image)), layout)
}

func (c *darwinScreenCapture) redrawImage(image uintptr, srcW, srcH, tw, th int) ([]byte, error) {
	api := c.api
	space := api.colorSpaceCreateRGB()
	if space == 0 {
		return nil, fmt.Errorf("%w: CGColorSpaceCreateDeviceRGB failed", ErrDarwinScreenCaptureUnavailable)
	}
	ctx := api.bitmapContextCreate(0, uint(tw), uint(th), 8, 0, space, darwinScreenContextBitmapInfo)
	api.colorSpaceRelease(space)
	if ctx == 0 {
		return nil, fmt.Errorf("%w: CGBitmapContextCreate %dx%d failed", ErrDarwinScreenCaptureUnavailable, tw, th)
	}
	defer api.contextRelease(ctx)
	quality := int32(darwinScreenInterpolationNone)
	if srcW != tw || srcH != th {
		quality = darwinScreenInterpolationMedium
	}
	api.contextSetInterpolation(ctx, quality)
	api.contextDrawImage(ctx, darwinScreenCGRect{W: float64(tw), H: float64(th)}, image)
	ptr := api.bitmapContextGetData(ctx)
	stride := int(api.bitmapContextBytesPerRow(ctx))
	if ptr == 0 || stride < tw*4 {
		return nil, fmt.Errorf("%w: bitmap context has no backing store", ErrDarwinScreenCaptureUnavailable)
	}
	src := darwinScreenBytes(ptr, stride*th)
	return darwinScreenConvertToRGBA(nil, src, tw, th, stride, darwinScreenLayoutRGBA)
}

func (c *darwinScreenCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.api.withPool(c.sck.release)
	c.hasLastHash = false
	return nil
}

// darwinScreenBytes views native (non-Go) memory as a byte slice. The double
// indirection avoids a direct uintptr->unsafe.Pointer conversion; the memory is
// owned by CoreFoundation/CoreGraphics and stays valid until it is released.
func darwinScreenBytes(ptr uintptr, n int) []byte {
	if ptr == 0 || n <= 0 {
		return nil
	}
	p := *(*unsafe.Pointer)(unsafe.Pointer(&ptr))
	return unsafe.Slice((*byte)(p), n)
}
