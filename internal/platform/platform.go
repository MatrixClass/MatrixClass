package platform

import "fmt"

type Desktop string

const (
	DesktopUnknown Desktop = "unknown"
	DesktopX11     Desktop = "x11"
	DesktopWayland Desktop = "wayland"
)

type CapabilityStatus string

const (
	StatusUnknown     CapabilityStatus = "unknown"
	StatusAvailable   CapabilityStatus = "available"
	StatusUnavailable CapabilityStatus = "unavailable"
	StatusRestricted  CapabilityStatus = "restricted"
)

type Capabilities struct {
	OS                    string           `json:"os"`
	Architecture          string           `json:"architecture"`
	OSVersion             string           `json:"os_version,omitempty"`
	HostArchitecture      string           `json:"host_architecture,omitempty"`
	Translated            bool             `json:"translated,omitempty"`
	Desktop               Desktop          `json:"desktop"`
	ScreenCapture         bool             `json:"screen_capture"`
	AudioCapture          bool             `json:"audio_capture"`
	RemoteInput           bool             `json:"remote_input"`
	ScreenLock            bool             `json:"screen_lock"`
	PowerControl          bool             `json:"power_control"`
	ProcessControl        bool             `json:"process_control"`
	AppDiscovery          bool             `json:"app_discovery"`
	CommandExecution      bool             `json:"command_execution"`
	SystemTray            bool             `json:"system_tray"`
	WaylandRestricted     bool             `json:"wayland_restricted"`
	ScreenCaptureStatus   CapabilityStatus `json:"screen_capture_status"`
	ScreenCaptureReason   string           `json:"screen_capture_reason,omitempty"`
	AudioCaptureStatus    CapabilityStatus `json:"audio_capture_status"`
	AudioCaptureReason    string           `json:"audio_capture_reason,omitempty"`
	RemoteInputStatus     CapabilityStatus `json:"remote_input_status"`
	RemoteInputReason     string           `json:"remote_input_reason,omitempty"`
	ScreenLockStatus      CapabilityStatus `json:"screen_lock_status"`
	ScreenLockReason      string           `json:"screen_lock_reason,omitempty"`
	ScreenRecordingStatus CapabilityStatus `json:"screen_recording_status"`
	ScreenRecordingReason string           `json:"screen_recording_reason,omitempty"`
	MicrophoneStatus      CapabilityStatus `json:"microphone_status"`
	MicrophoneReason      string           `json:"microphone_reason,omitempty"`
	AccessibilityStatus   CapabilityStatus `json:"accessibility_status"`
	AccessibilityReason   string           `json:"accessibility_reason,omitempty"`
}

func (c Capabilities) Summary() string {
	identity := c.OS + "/" + c.Architecture
	if c.OSVersion != "" {
		identity += " version=" + c.OSVersion
	}
	if c.Translated {
		identity += " rosetta(host=" + c.HostArchitecture + ")"
	}
	return fmt.Sprintf("%s desktop=%s screen=%s(%s) audio=%s(%s) input=%s(%s) lock=%s(%s)",
		identity, c.Desktop,
		c.ScreenCaptureStatus, c.ScreenCaptureReason,
		c.AudioCaptureStatus, c.AudioCaptureReason,
		c.RemoteInputStatus, c.RemoteInputReason,
		c.ScreenLockStatus, c.ScreenLockReason)
}
