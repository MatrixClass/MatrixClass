package media

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestDarwinScreenParseProductVersion(t *testing.T) {
	cases := []struct {
		in   string
		want darwinScreenOSVersion
		ok   bool
	}{
		{"14.4.1", darwinScreenOSVersion{14, 4, 1}, true},
		{"12.7", darwinScreenOSVersion{12, 7, 0}, true},
		{"26.0\n", darwinScreenOSVersion{26, 0, 0}, true},
		{"15", darwinScreenOSVersion{15, 0, 0}, true},
		{"", darwinScreenOSVersion{}, false},
		{"abc", darwinScreenOSVersion{}, false},
		{"14.x", darwinScreenOSVersion{}, false},
		{"0.1", darwinScreenOSVersion{}, false},
	}
	for _, tc := range cases {
		got, ok := darwinScreenParseProductVersion(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parse(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDarwinScreenVersionFromKernelRelease(t *testing.T) {
	cases := map[string]int{"21.6.0": 12, "22.1.0": 13, "23.4.0": 14, "24.0.0": 15, "25.0.0": 26, "20.6.0": 11}
	for in, major := range cases {
		got, ok := darwinScreenVersionFromKernelRelease(in)
		if !ok || got.Major != major {
			t.Errorf("kernel %q => %v,%v want major %d", in, got, ok, major)
		}
	}
	if got, ok := darwinScreenVersionFromKernelRelease("19.6.0"); !ok || got.Major != 10 || got.Minor != 15 {
		t.Errorf("kernel 19.6.0 => %v,%v want 10.15", got, ok)
	}
	if _, ok := darwinScreenVersionFromKernelRelease("garbage"); ok {
		t.Error("garbage kernel release parsed")
	}
}

func TestDarwinScreenResolveOSVersion(t *testing.T) {
	if v := darwinScreenResolveOSVersion("13.6.1", "23.0.0"); v.Major != 13 || v.Minor != 6 {
		t.Errorf("product version should win, got %v", v)
	}
	if v := darwinScreenResolveOSVersion("10.16", "23.4.0"); v.Major != 14 {
		t.Errorf("10.16 compat value should defer to kernel, got %v", v)
	}
	if v := darwinScreenResolveOSVersion("", "22.1.0"); v.Major != 13 {
		t.Errorf("missing product version should use kernel, got %v", v)
	}
	if v := darwinScreenResolveOSVersion("10.16", ""); v.Major != 11 {
		t.Errorf("compat value alone should map to 11, got %v", v)
	}
	if v := darwinScreenResolveOSVersion("", ""); v.Major != 0 {
		t.Errorf("unknown version should be zero, got %v", v)
	}
	if !(darwinScreenOSVersion{14, 0, 0}).AtLeast(14, 0) || (darwinScreenOSVersion{13, 9, 0}).AtLeast(14, 0) || !(darwinScreenOSVersion{26, 0, 0}).AtLeast(14, 2) {
		t.Error("AtLeast comparison wrong")
	}
}

func TestDarwinScreenLayoutFromBitmapInfo(t *testing.T) {
	cases := []struct {
		name     string
		info     uint32
		bpc, bpp int
		want     darwinScreenPixelLayout
	}{
		{"display BGRA premultiplied", darwinScreenAlphaPremultipliedFirst | darwinScreenByteOrder32Little, 8, 32, darwinScreenLayoutBGRA},
		{"display BGRX", darwinScreenAlphaNoneSkipFirst | darwinScreenByteOrder32Little, 8, 32, darwinScreenLayoutBGRA},
		{"little alpha last", darwinScreenAlphaPremultipliedLast | darwinScreenByteOrder32Little, 8, 32, darwinScreenLayoutABGR},
		{"default RGBA", darwinScreenAlphaPremultipliedLast, 8, 32, darwinScreenLayoutRGBA},
		{"big RGBX", darwinScreenAlphaNoneSkipLast | darwinScreenByteOrder32Big, 8, 32, darwinScreenLayoutRGBA},
		{"big ARGB", darwinScreenAlphaFirst | darwinScreenByteOrder32Big, 8, 32, darwinScreenLayoutARGB},
		{"float", darwinScreenAlphaPremultipliedLast | darwinScreenFloatComponents, 32, 128, darwinScreenLayoutUnknown},
		{"16 bit", darwinScreenAlphaPremultipliedLast, 16, 64, darwinScreenLayoutUnknown},
		{"24 bpp", darwinScreenAlphaNone, 8, 24, darwinScreenLayoutUnknown},
		{"alpha none 32", darwinScreenAlphaNone, 8, 32, darwinScreenLayoutUnknown},
		{"alpha only", darwinScreenAlphaOnly, 8, 32, darwinScreenLayoutUnknown},
		{"16 little order", darwinScreenAlphaPremultipliedFirst | darwinScreenByteOrder16Little, 8, 32, darwinScreenLayoutUnknown},
		{"packed pixel format", darwinScreenAlphaNoneSkipFirst | darwinScreenByteOrder32Little | 0x10000, 8, 32, darwinScreenLayoutUnknown},
	}
	for _, tc := range cases {
		if got := darwinScreenLayoutFromBitmapInfo(tc.info, tc.bpc, tc.bpp); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if darwinScreenContextBitmapInfo != 0x4005 {
		t.Errorf("context bitmap info = %#x, want 0x4005", darwinScreenContextBitmapInfo)
	}
	if darwinScreenLayoutFromBitmapInfo(darwinScreenContextBitmapInfo, 8, 32) != darwinScreenLayoutRGBA {
		t.Error("redraw context format must decode as RGBA")
	}
}

func TestDarwinScreenConvertBGRAWithStride(t *testing.T) {
	// 2x2 BGRA pixels with 4 bytes of row padding (stride 12) filled with 0xEE.
	src := []byte{
		1, 2, 3, 10, 4, 5, 6, 20, 0xEE, 0xEE, 0xEE, 0xEE,
		7, 8, 9, 30, 11, 12, 13, 0,
	}
	got, err := darwinScreenConvertToRGBA(nil, src, 2, 2, 12, darwinScreenLayoutBGRA)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		3, 2, 1, 255, 6, 5, 4, 255,
		9, 8, 7, 255, 13, 12, 11, 255,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestDarwinScreenConvertAllLayouts(t *testing.T) {
	// One pixel R=10 G=20 B=30 A=40 encoded in each layout.
	cases := map[darwinScreenPixelLayout][]byte{
		darwinScreenLayoutBGRA: {30, 20, 10, 40},
		darwinScreenLayoutRGBA: {10, 20, 30, 40},
		darwinScreenLayoutARGB: {40, 10, 20, 30},
		darwinScreenLayoutABGR: {40, 30, 20, 10},
	}
	for layout, px := range cases {
		got, err := darwinScreenConvertToRGBA(nil, px, 1, 1, 4, layout)
		if err != nil {
			t.Fatalf("%v: %v", layout, err)
		}
		if !bytes.Equal(got, []byte{10, 20, 30, 255}) {
			t.Errorf("%v: got %v", layout, got)
		}
	}
}

func TestDarwinScreenConvertRejectsBadInput(t *testing.T) {
	if _, err := darwinScreenConvertToRGBA(nil, make([]byte, 16), 2, 2, 7, darwinScreenLayoutBGRA); !errors.Is(err, errDarwinScreenBadGeometry) {
		t.Errorf("stride < width*4: %v", err)
	}
	// Last row may omit padding, but must contain width*4 bytes.
	if _, err := darwinScreenConvertToRGBA(nil, make([]byte, 12+8), 2, 2, 12, darwinScreenLayoutBGRA); err != nil {
		t.Errorf("unpadded last row should be accepted: %v", err)
	}
	if _, err := darwinScreenConvertToRGBA(nil, make([]byte, 12+7), 2, 2, 12, darwinScreenLayoutBGRA); !errors.Is(err, errDarwinScreenShortBuffer) {
		t.Errorf("short buffer: %v", err)
	}
	if _, err := darwinScreenConvertToRGBA(nil, make([]byte, 4), 1, 1, 4, darwinScreenLayoutUnknown); !errors.Is(err, errDarwinScreenBadLayout) {
		t.Errorf("unknown layout: %v", err)
	}
	if _, err := darwinScreenConvertToRGBA(nil, nil, 0, 1, 4, darwinScreenLayoutBGRA); !errors.Is(err, errDarwinScreenBadGeometry) {
		t.Errorf("zero width: %v", err)
	}
}

func TestDarwinScreenConvertReusesBuffer(t *testing.T) {
	dst := make([]byte, 0, 64)
	got, err := darwinScreenConvertToRGBA(dst, []byte{1, 2, 3, 4}, 1, 1, 4, darwinScreenLayoutBGRA)
	if err != nil || len(got) != 4 || &got[0] != &dst[:1][0] {
		t.Fatalf("expected reuse of dst capacity, err=%v", err)
	}
}

func TestDarwinScreenTargetSize(t *testing.T) {
	cases := []struct {
		srcW, srcH, maxW, maxH, wantW, wantH int
	}{
		{2880, 1800, 1920, 1080, 1728, 1080}, // Retina MacBook, height bound
		{5120, 2880, 1920, 1080, 1920, 1080}, // 5K iMac
		{1440, 900, 1920, 1080, 1440, 900},   // already fits: untouched
		{1441, 901, 0, 0, 1441, 901},         // no bound: untouched (odd OK)
		{3024, 1964, 1280, 0, 1280, 830},     // width-only bound
		{0, 100, 10, 10, 0, 0},
		{10000, 5000, 0, 0, 8192, 4096}, // clamped to codec limits
	}
	for _, tc := range cases {
		w, h := darwinScreenTargetSize(tc.srcW, tc.srcH, tc.maxW, tc.maxH)
		if w != tc.wantW || h != tc.wantH {
			t.Errorf("target(%d,%d,%d,%d) = %dx%d want %dx%d", tc.srcW, tc.srcH, tc.maxW, tc.maxH, w, h, tc.wantW, tc.wantH)
		}
		if w > 0 && !ValidVideoDimensions(w, h) {
			t.Errorf("target %dx%d exceeds video limits", w, h)
		}
	}
}

func TestDarwinScreenNeedsRedraw(t *testing.T) {
	if darwinScreenNeedsRedraw(darwinScreenLayoutBGRA, 100, 50, 100, 50) {
		t.Error("known layout at native size should copy directly")
	}
	if !darwinScreenNeedsRedraw(darwinScreenLayoutBGRA, 200, 100, 100, 50) {
		t.Error("resampling requires redraw")
	}
	if !darwinScreenNeedsRedraw(darwinScreenLayoutUnknown, 100, 50, 100, 50) {
		t.Error("unknown layout requires redraw")
	}
}

func TestDarwinScreenPermissionTracker(t *testing.T) {
	var tr darwinScreenPermissionTracker
	now := time.Unix(1000, 0)
	if !tr.NeedsCheck(now) {
		t.Fatal("unknown state must be checked")
	}
	state, req := tr.Observe(false, true, now)
	if state != darwinScreenPermissionRequested || !req {
		t.Fatalf("first denial should request: %v %v", state, req)
	}
	state, req = tr.Observe(false, true, now)
	if state != darwinScreenPermissionRequested || req {
		t.Fatalf("request must happen only once: %v %v", state, req)
	}
	if tr.NeedsCheck(now.Add(time.Second)) {
		t.Error("denied state should be throttled")
	}
	if !tr.NeedsCheck(now.Add(darwinScreenPermissionRecheck)) {
		t.Error("denied state should be rechecked after the interval")
	}
	state, _ = tr.Observe(true, true, now.Add(3*time.Second))
	if state != darwinScreenPermissionGranted || tr.NeedsCheck(now.Add(time.Hour)) {
		t.Fatalf("granted should stick without rechecks: %v", state)
	}
	tr.Revoke(now.Add(4 * time.Second))
	if tr.State() != darwinScreenPermissionDenied {
		t.Fatalf("revoke => %v", tr.State())
	}

	var noPrompt darwinScreenPermissionTracker
	if state, req := noPrompt.Observe(false, false, now); state != darwinScreenPermissionDenied || req {
		t.Fatalf("probe without prompting: %v %v", state, req)
	}
}

func TestDarwinScreenPlannerSelectsBackendByVersion(t *testing.T) {
	cases := []struct {
		major    int
		sck, cg  bool
		expected darwinScreenBackendKind
	}{
		{12, false, true, darwinScreenBackendCG},
		{13, true, true, darwinScreenBackendCG}, // SCK present but no screenshot API
		{14, true, true, darwinScreenBackendSCK},
		{15, true, false, darwinScreenBackendSCK},
		{26, true, true, darwinScreenBackendSCK},
		{14, false, true, darwinScreenBackendCG},
		{14, false, false, darwinScreenBackendNone},
	}
	for _, tc := range cases {
		p := newDarwinScreenPlanner(darwinScreenOSVersion{Major: tc.major}, tc.sck, tc.cg)
		if got := p.Backend(); got != tc.expected {
			t.Errorf("macOS %d sck=%v cg=%v => %v want %v", tc.major, tc.sck, tc.cg, got, tc.expected)
		}
	}
	if darwinScreenBackendSCK.CaptureBackend() != darwinScreenCaptureBackendSCK || darwinScreenBackendNone.CaptureBackend() != CaptureBackendUnavailable {
		t.Error("backend name mapping wrong")
	}
}

func TestDarwinScreenPlannerFallbackAndRecovery(t *testing.T) {
	p := newDarwinScreenPlanner(darwinScreenOSVersion{Major: 14}, true, true)
	if p.ReportFailure(darwinScreenBackendSCK, darwinScreenErrPermission) || p.Backend() != darwinScreenBackendSCK {
		t.Fatal("permission errors must not switch backends")
	}
	for i := 1; i < darwinScreenSCKMaxFailures; i++ {
		if p.ReportFailure(darwinScreenBackendSCK, darwinScreenErrTransient) {
			t.Fatalf("switched too early after %d failures", i)
		}
	}
	p.ReportSuccess(darwinScreenBackendSCK)
	for i := 1; i < darwinScreenSCKMaxFailures; i++ {
		p.ReportFailure(darwinScreenBackendSCK, darwinScreenErrTransient)
	}
	if p.Backend() != darwinScreenBackendSCK {
		t.Fatal("success should reset the failure count")
	}
	if !p.ReportFailure(darwinScreenBackendSCK, darwinScreenErrTransient) || p.Backend() != darwinScreenBackendCG {
		t.Fatal("expected fallback to CoreGraphics after repeated failures")
	}
	if p.ReportFailure(darwinScreenBackendCG, darwinScreenErrTransient) {
		t.Fatal("CG failures do not change backend")
	}
	p.ResetForDisplayChange()
	if p.Backend() != darwinScreenBackendSCK {
		t.Fatal("display change should retry ScreenCaptureKit")
	}
	if !p.ReportFailure(darwinScreenBackendSCK, darwinScreenErrFatal) || p.Backend() != darwinScreenBackendCG {
		t.Fatal("fatal SCK error should fall back immediately")
	}

	noCG := newDarwinScreenPlanner(darwinScreenOSVersion{Major: 15}, true, false)
	for i := 0; i < 5; i++ {
		if noCG.ReportFailure(darwinScreenBackendSCK, darwinScreenErrTransient) {
			t.Fatal("without CG the backend cannot change")
		}
	}
	if noCG.Backend() != darwinScreenBackendSCK {
		t.Fatal("without CG keep retrying SCK")
	}
}

func TestDarwinScreenClassifySCKError(t *testing.T) {
	d := darwinScreenSCKErrorDomain
	cases := []struct {
		domain string
		code   int
		want   darwinScreenErrKind
	}{
		{d, -3801, darwinScreenErrPermission},
		{d, -3803, darwinScreenErrFatal},
		{d, -3814, darwinScreenErrDisplayGone},
		{d, -3815, darwinScreenErrDisplayGone},
		{d, -3811, darwinScreenErrTransient},
		{"NSCocoaErrorDomain", -3801, darwinScreenErrTransient},
	}
	for _, tc := range cases {
		if got := darwinScreenClassifySCKError(tc.domain, tc.code); got != tc.want {
			t.Errorf("%s %d => %v want %v", tc.domain, tc.code, got, tc.want)
		}
	}
}

func TestDarwinScreenDisplayTracker(t *testing.T) {
	var d darwinScreenDisplayTracker
	if a, b := d.Update(1, 2880, 1800); a || b {
		t.Fatal("first observation is not a change")
	}
	if a, b := d.Update(1, 2880, 1800); a || b {
		t.Fatal("same display reported as change")
	}
	if a, b := d.Update(1, 1920, 1200); a || !b {
		t.Fatal("resolution change not detected")
	}
	if a, _ := d.Update(2, 1920, 1200); !a {
		t.Fatal("main display change not detected")
	}
}
