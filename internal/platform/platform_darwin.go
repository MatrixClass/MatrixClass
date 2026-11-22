//go:build darwin

package platform

import (
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"
)

const darwinRTLDNow = 2

func Detect() Capabilities {
	screenStatus, screenReason := detectDarwinScreenRecording()
	accessibilityStatus, accessibilityReason := detectDarwinAccessibility()
	microphoneStatus, microphoneReason := detectDarwinMicrophone()
	version := DarwinProductVersion()
	translated := DarwinRunningUnderRosetta()

	return Capabilities{
		OS: runtime.GOOS, Architecture: runtime.GOARCH,
		OSVersion: version, HostArchitecture: darwinHostArchitecture(translated), Translated: translated,
		ScreenCapture: screenStatus == StatusAvailable, AudioCapture: microphoneStatus == StatusAvailable,
		RemoteInput: accessibilityStatus == StatusAvailable,
		ScreenLock:  true, PowerControl: true, ProcessControl: true,
		AppDiscovery: true, CommandExecution: true, SystemTray: true,
		ScreenCaptureStatus: screenStatus, ScreenCaptureReason: screenReason,
		AudioCaptureStatus: microphoneStatus, AudioCaptureReason: microphoneReason,
		RemoteInputStatus: accessibilityStatus, RemoteInputReason: accessibilityReason,
		ScreenLockStatus: StatusAvailable, ScreenLockReason: darwinLockReason(accessibilityStatus),
		ScreenRecordingStatus: screenStatus, ScreenRecordingReason: screenReason,
		MicrophoneStatus: microphoneStatus, MicrophoneReason: microphoneReason,
		AccessibilityStatus: accessibilityStatus, AccessibilityReason: accessibilityReason,
	}
}

func darwinLockReason(accessibility CapabilityStatus) string {
	if accessibility == StatusAvailable {
		return "全屏遮罩 + 演示模式锁定（禁用切换应用/强制退出），并拦截 Cmd+Q 等快捷键"
	}
	return "全屏遮罩 + 演示模式锁定；授予辅助功能权限后才能拦截 Cmd+Q 等快捷键"
}

// DarwinProductVersion returns the macOS product version (e.g. "14.5"), or
// "" when it cannot be determined.
func DarwinProductVersion() string {
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}

// DarwinRunningUnderRosetta reports whether this (amd64) process is being
// translated by Rosetta 2 on Apple Silicon.
func DarwinRunningUnderRosetta() bool {
	value, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && value == 1
}

func darwinHostArchitecture(translated bool) string {
	if translated {
		return "arm64"
	}
	return runtime.GOARCH
}

// darwinNative holds lazily resolved TCC-related entry points. Every field
// may be zero/nil when the symbol is unavailable.
type darwinNative struct {
	preflightScreen      func() bool
	requestScreen        func() bool
	axTrusted            func() bool
	axTrustedWithOptions func(uintptr) bool
	cfDictionaryCreate   func(allocator uintptr, keys, values unsafe.Pointer, count int, keyCallbacks, valueCallbacks uintptr) uintptr
	cfRelease            func(uintptr)
	axPromptKey          uintptr // CFStringRef kAXTrustedCheckOptionPrompt
	cfBooleanTrue        uintptr // CFBooleanRef kCFBooleanTrue
	dictKeyCallbacks     uintptr // &kCFTypeDictionaryKeyCallBacks
	dictValueCallbacks   uintptr // &kCFTypeDictionaryValueCallBacks
	avCaptureDevice      objc.ID
	avMediaTypeAudio     objc.ID
}

var loadDarwinNative = sync.OnceValue(func() *darwinNative {
	n := &darwinNative{}
	lookup := func(handle uintptr, name string) uintptr {
		if handle == 0 {
			return 0
		}
		address, err := purego.Dlsym(handle, name)
		if err != nil {
			return 0
		}
		return address
	}
	open := func(path string) uintptr {
		handle, err := purego.Dlopen(path, darwinRTLDNow|purego.RTLD_GLOBAL)
		if err != nil {
			return 0
		}
		return handle
	}
	graphics := open("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
	if address := lookup(graphics, "CGPreflightScreenCaptureAccess"); address != 0 {
		purego.RegisterFunc(&n.preflightScreen, address)
	}
	if address := lookup(graphics, "CGRequestScreenCaptureAccess"); address != 0 {
		purego.RegisterFunc(&n.requestScreen, address)
	}
	services := open("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
	if address := lookup(services, "AXIsProcessTrusted"); address != 0 {
		purego.RegisterFunc(&n.axTrusted, address)
	}
	if address := lookup(services, "AXIsProcessTrustedWithOptions"); address != 0 {
		purego.RegisterFunc(&n.axTrustedWithOptions, address)
	}
	n.axPromptKey = darwinConstant(lookup(services, "kAXTrustedCheckOptionPrompt"))
	foundation := open("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	if address := lookup(foundation, "CFDictionaryCreate"); address != 0 {
		purego.RegisterFunc(&n.cfDictionaryCreate, address)
	}
	if address := lookup(foundation, "CFRelease"); address != 0 {
		purego.RegisterFunc(&n.cfRelease, address)
	}
	n.cfBooleanTrue = darwinConstant(lookup(foundation, "kCFBooleanTrue"))
	n.dictKeyCallbacks = lookup(foundation, "kCFTypeDictionaryKeyCallBacks")
	n.dictValueCallbacks = lookup(foundation, "kCFTypeDictionaryValueCallBacks")
	avFoundation := open("/System/Library/Frameworks/AVFoundation.framework/AVFoundation")
	if avFoundation != 0 {
		n.avCaptureDevice = objc.ID(objc.GetClass("AVCaptureDevice"))
		n.avMediaTypeAudio = objc.ID(darwinConstant(lookup(avFoundation, "AVMediaTypeAudio")))
	}
	return n
})

// darwinConstant dereferences an exported pointer-sized constant such as a
// CFStringRef symbol. The double indirection keeps go vet's unsafeptr check
// satisfied: the symbol address comes from dlsym, not from Go memory.
func darwinConstant(address uintptr) uintptr {
	if address == 0 {
		return 0
	}
	return *(*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&address)))
}

func detectDarwinScreenRecording() (CapabilityStatus, string) {
	n := loadDarwinNative()
	if n.preflightScreen == nil {
		return StatusUnknown, "当前 macOS 未提供 CGPreflightScreenCaptureAccess，无法查询屏幕录制权限"
	}
	if n.preflightScreen() {
		return StatusAvailable, "屏幕录制权限已授予"
	}
	return StatusRestricted, "需要在系统设置>隐私与安全性>屏幕与系统录音中允许访问，授权后需重启应用"
}

func detectDarwinAccessibility() (CapabilityStatus, string) {
	n := loadDarwinNative()
	if n.axTrusted == nil {
		return StatusUnknown, "当前 macOS 未提供 AXIsProcessTrusted，无法查询辅助功能权限"
	}
	if n.axTrusted() {
		return StatusAvailable, "辅助功能权限已授予"
	}
	return StatusRestricted, "需要在系统设置>隐私与安全性>辅助功能中允许访问"
}

func detectDarwinMicrophone() (CapabilityStatus, string) {
	n := loadDarwinNative()
	if n.avCaptureDevice == 0 || n.avMediaTypeAudio == 0 {
		return StatusUnknown, "无法加载 AVFoundation，无法查询麦克风权限"
	}
	code := objc.Send[int64](n.avCaptureDevice, objc.RegisterName("authorizationStatusForMediaType:"), n.avMediaTypeAudio)
	return avAuthorizationStatus(code)
}
