//go:build !darwin

package platform

// PermissionStatus reports StatusAvailable: Windows and Linux have no TCC
// style per-app permission gate that MatrixClass can query. Linux desktop
// limitations (e.g. Wayland) are reported through Detect instead.
func PermissionStatus(p Permission) (CapabilityStatus, string) {
	switch p {
	case PermissionScreenRecording, PermissionAccessibility, PermissionMicrophone, PermissionAutomation:
		return StatusAvailable, "当前平台无需单独授权"
	default:
		return StatusUnavailable, "未知权限"
	}
}

// RequestPermission is a no-op outside macOS.
func RequestPermission(p Permission) (CapabilityStatus, string) { return PermissionStatus(p) }

// OpenPermissionSettings is only implemented on macOS.
func OpenPermissionSettings(Permission) error { return ErrPermissionUnsupported }

// PreferredLogDir returns "" outside macOS, meaning "keep the caller's
// default" (currently <exe dir>/logs).
func PreferredLogDir() string { return "" }

// PreferredDataDir returns "" outside macOS, meaning "keep the caller's default".
func PreferredDataDir() string { return "" }
