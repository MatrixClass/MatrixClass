//go:build windows

package platform

import "runtime"

func Detect() Capabilities {
	return Capabilities{
		OS:                runtime.GOOS,
		Architecture:      runtime.GOARCH,
		ScreenCapture:     true,
		AudioCapture:      true,
		RemoteInput:       true,
		ScreenLock:        true,
		PowerControl:      true,
		ProcessControl:    true,
		AppDiscovery:      true,
		CommandExecution:  true,
		SystemTray:        true,
		WaylandRestricted: false,
	}
}
