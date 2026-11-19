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
