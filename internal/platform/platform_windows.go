//go:build windows

package platform

import "runtime"

func Detect() Capabilities {
	return Capabilities{
		OS:                  runtime.GOOS,
		Architecture:        runtime.GOARCH,
		ScreenCapture:       true,
		AudioCapture:        true,
		RemoteInput:         true,
		ScreenLock:          true,
		PowerControl:        true,
		ProcessControl:      true,
		AppDiscovery:        true,
		CommandExecution:    true,
		SystemTray:          true,
		WaylandRestricted:   false,
		ScreenCaptureStatus: StatusAvailable,
		ScreenCaptureReason: "DXGI/GDI 屏幕采集",
		AudioCaptureStatus:  StatusAvailable,
		AudioCaptureReason:  "Windows 音频设备",
		RemoteInputStatus:   StatusAvailable,
		RemoteInputReason:   "Windows SendInput",
		ScreenLockStatus:    StatusAvailable,
		ScreenLockReason:    "Windows 锁屏控制",
	}
}
