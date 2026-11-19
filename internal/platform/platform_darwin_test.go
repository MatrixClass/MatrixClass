//go:build darwin

package platform

import "testing"

func TestDarwinDetectReportsTCCPermissionStates(t *testing.T) {
	capabilities := Detect()
	if capabilities.OS != "darwin" {
		t.Fatalf("expected darwin runtime identity, got %q", capabilities.OS)
	}
	if capabilities.ScreenRecordingStatus == "" {
		t.Fatal("expected Screen Recording permission status")
	}
	if capabilities.MicrophoneStatus == "" {
		t.Fatal("expected Microphone permission status")
	}
	if capabilities.AccessibilityStatus == "" {
		t.Fatal("expected Accessibility permission status")
	}
	if capabilities.ScreenCaptureStatus != capabilities.ScreenRecordingStatus {
		t.Fatalf("screen capture status must mirror Screen Recording status: capture=%q recording=%q", capabilities.ScreenCaptureStatus, capabilities.ScreenRecordingStatus)
	}
	if capabilities.AudioCaptureStatus != capabilities.MicrophoneStatus {
		t.Fatalf("audio capture status must mirror Microphone status: audio=%q microphone=%q", capabilities.AudioCaptureStatus, capabilities.MicrophoneStatus)
	}
	if capabilities.RemoteInputStatus != capabilities.AccessibilityStatus {
		t.Fatalf("remote input status must mirror Accessibility status: input=%q accessibility=%q", capabilities.RemoteInputStatus, capabilities.AccessibilityStatus)
	}
}

func TestDarwinPermissionStatusIsKnown(t *testing.T) {
	capabilities := Detect()
	statuses := []CapabilityStatus{
		capabilities.ScreenRecordingStatus,
		capabilities.MicrophoneStatus,
		capabilities.AccessibilityStatus,
	}
	for _, status := range statuses {
		switch status {
		case StatusUnknown, StatusAvailable, StatusUnavailable, StatusRestricted:
		default:
			t.Fatalf("unexpected Darwin permission status %q", status)
		}
	}
}
