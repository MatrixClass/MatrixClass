//go:build darwin

package platform

import (
	"runtime"

	"github.com/ebitengine/purego"
)

const darwinRTLDNow = 2

func Detect() Capabilities {
	screenStatus, screenReason := detectDarwinScreenRecording()
	accessibilityStatus, accessibilityReason := detectDarwinAccessibility()
	microphoneStatus := StatusRestricted
	microphoneReason := "需要在系统设置中允许麦克风访问；当前仅能安全提示用户授权"

	return Capabilities{
		OS: runtime.GOOS, Architecture: runtime.GOARCH,
		ScreenCapture: screenStatus == StatusAvailable, AudioCapture: false,
		RemoteInput: accessibilityStatus == StatusAvailable,
		ScreenLock:  true, PowerControl: true, ProcessControl: true,
		AppDiscovery: true, CommandExecution: true, SystemTray: true,
		ScreenCaptureStatus: screenStatus, ScreenCaptureReason: screenReason,
		AudioCaptureStatus: microphoneStatus, AudioCaptureReason: microphoneReason,
		RemoteInputStatus: accessibilityStatus, RemoteInputReason: accessibilityReason,
		ScreenLockStatus: StatusAvailable, ScreenLockReason: "macOS 登录会话锁屏能力由系统提供",
		ScreenRecordingStatus: screenStatus, ScreenRecordingReason: screenReason,
		MicrophoneStatus: microphoneStatus, MicrophoneReason: microphoneReason,
		AccessibilityStatus: accessibilityStatus, AccessibilityReason: accessibilityReason,
	}
}

func detectDarwinScreenRecording() (CapabilityStatus, string) {
	handle, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", darwinRTLDNow)
	if err != nil {
		return StatusUnknown, "无法加载 CoreGraphics，无法查询 Screen Recording 权限"
	}
	defer purego.Dlclose(handle)
	address, err := purego.Dlsym(handle, "CGPreflightScreenCaptureAccess")
	if err != nil || address == 0 {
		return StatusUnknown, "当前 macOS 未提供 CGPreflightScreenCaptureAccess"
	}
	var preflight func() bool
	purego.RegisterFunc(&preflight, address)
	if preflight() {
		return StatusAvailable, "Screen Recording 权限已授予"
	}
	return StatusRestricted, "需要在系统设置>隐私与安全性>屏幕与系统录音中允许访问"
}

func detectDarwinAccessibility() (CapabilityStatus, string) {
	handle, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", darwinRTLDNow)
	if err != nil {
		return StatusUnknown, "无法加载 ApplicationServices，无法查询 Accessibility 权限"
	}
	defer purego.Dlclose(handle)
	address, err := purego.Dlsym(handle, "AXIsProcessTrusted")
	if err != nil || address == 0 {
		return StatusUnknown, "当前 macOS 未提供 AXIsProcessTrusted"
	}
	var trusted func() bool
	purego.RegisterFunc(&trusted, address)
	if trusted() {
		return StatusAvailable, "Accessibility 权限已授予"
	}
	return StatusRestricted, "需要在系统设置>隐私与安全性>辅助功能中允许访问"
}
