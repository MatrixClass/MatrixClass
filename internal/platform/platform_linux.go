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
	return Capabilities{
		OS:                runtime.GOOS,
		Architecture:      runtime.GOARCH,
		Desktop:           desktop,
		ScreenCapture:     desktop == DesktopX11,
		AudioCapture:      commandExists("pw-cli") || commandExists("pactl") || commandExists("arecord"),
		RemoteInput:       desktop == DesktopX11,
		ScreenLock:        commandExists("loginctl") || commandExists("xdg-screensaver"),
		PowerControl:      commandExists("systemctl") || commandExists("shutdown"),
		ProcessControl:    true,
		AppDiscovery:      true,
		CommandExecution:  true,
		SystemTray:        desktop == DesktopX11,
		WaylandRestricted: wayland,
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
