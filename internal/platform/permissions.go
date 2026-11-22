package platform

import "errors"

// Permission identifies an OS privacy permission (macOS TCC) that a
// MatrixClass feature depends on. On platforms without such a permission
// model every query reports StatusAvailable/unsupported as documented on the
// individual functions.
type Permission string

const (
	// PermissionScreenRecording is required for screen capture/broadcast
	// monitoring (macOS: Privacy & Security > Screen & System Audio Recording).
	PermissionScreenRecording Permission = "screen_recording"
	// PermissionAccessibility is required for remote input injection and the
	// classroom-lock shortcut guard (macOS: Privacy & Security > Accessibility).
	PermissionAccessibility Permission = "accessibility"
	// PermissionMicrophone is required for voice/audio capture.
	PermissionMicrophone Permission = "microphone"
	// PermissionAutomation covers Apple Events sent through osascript (used for
	// non-root shutdown/restart/log out on macOS).
	PermissionAutomation Permission = "automation"
)

// ErrPermissionUnsupported is returned when the current platform has no
// settings page or request flow for the given permission.
var ErrPermissionUnsupported = errors.New("platform: permission management is not supported on this platform")

// AllPermissions lists the permissions in the order a setup guide should
// present them.
func AllPermissions() []Permission {
	return []Permission{PermissionScreenRecording, PermissionAccessibility, PermissionMicrophone, PermissionAutomation}
}

// PermissionSettingsURL returns the macOS System Settings deep link for a
// permission pane. The URLs work on macOS 10.15 through current releases
// (System Settings keeps accepting the legacy System Preferences anchors).
func PermissionSettingsURL(p Permission) string {
	const base = "x-apple.systempreferences:com.apple.preference.security?"
	switch p {
	case PermissionScreenRecording:
		return base + "Privacy_ScreenCapture"
	case PermissionAccessibility:
		return base + "Privacy_Accessibility"
	case PermissionMicrophone:
		return base + "Privacy_Microphone"
	case PermissionAutomation:
		return base + "Privacy_Automation"
	default:
		return ""
	}
}

// PermissionLabel returns a short user-facing (Chinese) name for the
// permission as shown in macOS System Settings.
func PermissionLabel(p Permission) string {
	switch p {
	case PermissionScreenRecording:
		return "屏幕与系统录音"
	case PermissionAccessibility:
		return "辅助功能"
	case PermissionMicrophone:
		return "麦克风"
	case PermissionAutomation:
		return "自动化"
	default:
		return string(p)
	}
}

// MissingPermissions returns the permissions whose status in c is not
// available (restricted or unknown), in guide order. Automation is not
// tracked in Capabilities and is never reported here.
func MissingPermissions(c Capabilities) []Permission {
	var missing []Permission
	check := func(p Permission, status CapabilityStatus) {
		if status == StatusRestricted || status == StatusUnknown {
			missing = append(missing, p)
		}
	}
	check(PermissionScreenRecording, c.ScreenRecordingStatus)
	check(PermissionAccessibility, c.AccessibilityStatus)
	check(PermissionMicrophone, c.MicrophoneStatus)
	return missing
}

// avAuthorizationStatus maps AVAuthorizationStatus (AVFoundation) to a
// capability status and reason.
func avAuthorizationStatus(code int64) (CapabilityStatus, string) {
	switch code {
	case 3: // AVAuthorizationStatusAuthorized
		return StatusAvailable, "麦克风权限已授予"
	case 2: // AVAuthorizationStatusDenied
		return StatusRestricted, "麦克风权限被拒绝，请在系统设置>隐私与安全性>麦克风中允许访问"
	case 1: // AVAuthorizationStatusRestricted
		return StatusUnavailable, "麦克风访问被系统策略（家长控制/MDM）限制"
	case 0: // AVAuthorizationStatusNotDetermined
		return StatusRestricted, "尚未请求麦克风权限，首次使用时系统会弹出授权提示"
	default:
		return StatusUnknown, "无法识别的麦克风授权状态"
	}
}
