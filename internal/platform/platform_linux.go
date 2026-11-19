//go:build linux

package platform

import (
	"os"
	"os/exec"
	"runtime"
)

func Detect() Capabilities {
	desktop := DesktopUnknown
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	x11 := os.Getenv("DISPLAY") != ""
	if wayland {
		desktop = DesktopWayland
	} else if x11 {
		desktop = DesktopX11
	}
	screenStatus, screenReason := StatusUnavailable, "未检测到 X11 DISPLAY"
	if x11 {
		screenStatus, screenReason = StatusAvailable, "X11 屏幕采集可初始化"
	} else if wayland {
		screenStatus, screenReason = StatusRestricted, "Wayland 需要桌面授权，当前未接入 Portal/PipeWire 采集"
	}
	audioAvailable := commandExists("pw-cat") || commandExists("parec") || commandExists("arecord")
	audioStatus, audioReason := StatusUnavailable, "未找到 pw-cat、parec 或 arecord"
	if audioAvailable {
		audioStatus, audioReason = StatusAvailable, "检测到 Linux PCM 音频命令"
	}
	inputStatus, inputReason := StatusUnavailable, "需要 X11 DISPLAY 和 libXtst.so.6"
	if x11 {
		inputStatus, inputReason = StatusAvailable, "X11 远程输入可初始化"
	} else if wayland {
		inputStatus, inputReason = StatusRestricted, "Wayland 默认禁止无授权远程输入"
	}
	lockAvailable := commandExists("loginctl") || commandExists("xdg-screensaver")
	lockStatus, lockReason := StatusUnavailable, "未找到 loginctl 或 xdg-screensaver"
	if lockAvailable {
		lockStatus, lockReason = StatusAvailable, "检测到桌面锁屏命令"
	}
	return Capabilities{
		OS:                    runtime.GOOS,
		Architecture:          runtime.GOARCH,
		Desktop:               desktop,
		ScreenCapture:         screenStatus == StatusAvailable,
		AudioCapture:          audioAvailable,
		RemoteInput:           inputStatus == StatusAvailable,
		ScreenLock:            lockAvailable,
		PowerControl:          commandExists("systemctl") || commandExists("shutdown"),
		ProcessControl:        true,
		AppDiscovery:          true,
		CommandExecution:      true,
		SystemTray:            desktop == DesktopX11,
		WaylandRestricted:     wayland,
		ScreenCaptureStatus:   screenStatus,
		ScreenCaptureReason:   screenReason,
		AudioCaptureStatus:    audioStatus,
		AudioCaptureReason:    audioReason,
		RemoteInputStatus:     inputStatus,
		RemoteInputReason:     inputReason,
		ScreenLockStatus:      lockStatus,
		ScreenLockReason:      lockReason,
		ScreenRecordingStatus: screenStatus,
		ScreenRecordingReason: screenReason,
		MicrophoneStatus:      audioStatus,
		MicrophoneReason:      audioReason,
		AccessibilityStatus:   inputStatus,
		AccessibilityReason:   inputReason,
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
