//go:build !linux && !windows && !darwin

package platform

import "runtime"

func Detect() Capabilities {
	return Capabilities{
		OS: runtime.GOOS, Architecture: runtime.GOARCH,
		ScreenCaptureStatus: StatusUnavailable, ScreenCaptureReason: "当前平台未实现屏幕采集",
		AudioCaptureStatus: StatusUnavailable, AudioCaptureReason: "当前平台未实现音频采集",
		RemoteInputStatus: StatusUnavailable, RemoteInputReason: "当前平台未实现远程输入",
		ScreenLockStatus: StatusUnavailable, ScreenLockReason: "当前平台未实现桌面锁屏",
		ScreenRecordingStatus: StatusUnavailable, ScreenRecordingReason: "当前平台未实现 Screen Recording 权限检测",
		MicrophoneStatus: StatusUnavailable, MicrophoneReason: "当前平台未实现麦克风权限检测",
		AccessibilityStatus: StatusUnavailable, AccessibilityReason: "当前平台未实现辅助功能权限检测",
	}
}
