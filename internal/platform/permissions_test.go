package platform

import (
	"reflect"
	"strings"
	"testing"
)

func TestPermissionSettingsURL(t *testing.T) {
	want := map[Permission]string{
		PermissionScreenRecording: "Privacy_ScreenCapture",
		PermissionAccessibility:   "Privacy_Accessibility",
		PermissionMicrophone:      "Privacy_Microphone",
		PermissionAutomation:      "Privacy_Automation",
	}
	for _, p := range AllPermissions() {
		url := PermissionSettingsURL(p)
		if !strings.HasPrefix(url, "x-apple.systempreferences:com.apple.preference.security?") || !strings.HasSuffix(url, want[p]) {
			t.Fatalf("PermissionSettingsURL(%s) = %q", p, url)
		}
		if PermissionLabel(p) == string(p) {
			t.Fatalf("missing label for %s", p)
		}
	}
	if PermissionSettingsURL("bogus") != "" {
		t.Fatal("expected empty URL for unknown permission")
	}
}

func TestMissingPermissions(t *testing.T) {
	c := Capabilities{
		ScreenRecordingStatus: StatusRestricted,
		AccessibilityStatus:   StatusAvailable,
		MicrophoneStatus:      StatusUnknown,
	}
	got := MissingPermissions(c)
	if !reflect.DeepEqual(got, []Permission{PermissionScreenRecording, PermissionMicrophone}) {
		t.Fatalf("MissingPermissions = %v", got)
	}
	if got := MissingPermissions(Capabilities{ScreenRecordingStatus: StatusAvailable, AccessibilityStatus: StatusAvailable, MicrophoneStatus: StatusUnavailable}); len(got) != 0 {
		t.Fatalf("unavailable (policy-blocked) permissions must not be listed as grantable: %v", got)
	}
}

func TestAVAuthorizationStatusMapping(t *testing.T) {
	cases := map[int64]CapabilityStatus{0: StatusRestricted, 1: StatusUnavailable, 2: StatusRestricted, 3: StatusAvailable, 9: StatusUnknown}
	for code, want := range cases {
		if got, reason := avAuthorizationStatus(code); got != want || reason == "" {
			t.Fatalf("avAuthorizationStatus(%d) = %q %q, want %q", code, got, reason, want)
		}
	}
}

func TestPermissionStatusKnownValues(t *testing.T) {
	for _, p := range AllPermissions() {
		status, _ := PermissionStatus(p)
		switch status {
		case StatusAvailable, StatusRestricted, StatusUnknown, StatusUnavailable:
		default:
			t.Fatalf("PermissionStatus(%s) = %q", p, status)
		}
	}
}
