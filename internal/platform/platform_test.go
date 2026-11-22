package platform

import (
	"strings"
	"testing"
)

func TestDetectReturnsRuntimeIdentity(t *testing.T) {
	capabilities := Detect()
	if capabilities.OS == "" || capabilities.Architecture == "" {
		t.Fatalf("expected runtime identity, got %+v", capabilities)
	}
}

func TestCapabilitySummaryExplainsRestrictedDesktop(t *testing.T) {
	capabilities := Capabilities{
		OS:                  "linux",
		Architecture:        "arm64",
		Desktop:             DesktopWayland,
		ScreenCapture:       false,
		ScreenCaptureStatus: StatusRestricted,
		ScreenCaptureReason: "桌面授权尚未完成",
		WaylandRestricted:   true,
	}
	summary := capabilities.Summary()
	if !strings.Contains(summary, "wayland") || !strings.Contains(summary, "桌面授权尚未完成") {
		t.Fatalf("summary does not explain Wayland limitation: %q", summary)
	}
}

func TestCapabilitySummaryIncludesVersionAndRosetta(t *testing.T) {
	summary := Capabilities{OS: "darwin", Architecture: "amd64", OSVersion: "14.5", Translated: true, HostArchitecture: "arm64"}.Summary()
	if !strings.HasPrefix(summary, "darwin/amd64 version=14.5 rosetta(host=arm64)") {
		t.Fatalf("summary = %q", summary)
	}
	if plain := (Capabilities{OS: "linux", Architecture: "arm64"}).Summary(); !strings.HasPrefix(plain, "linux/arm64 desktop=") {
		t.Fatalf("summary without version = %q", plain)
	}
}
