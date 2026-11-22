//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// PermissionStatus queries the current TCC state of p without prompting.
// Screen Recording grants only take effect after the app restarts, so a UI
// should poll this and suggest a restart once it flips to available.
func PermissionStatus(p Permission) (CapabilityStatus, string) {
	switch p {
	case PermissionScreenRecording:
		return detectDarwinScreenRecording()
	case PermissionAccessibility:
		return detectDarwinAccessibility()
	case PermissionMicrophone:
		return detectDarwinMicrophone()
	case PermissionAutomation:
		return StatusUnknown, "macOS 不提供自动化权限的静默查询；首次关机/重启/注销时系统会弹出授权提示"
	default:
		return StatusUnavailable, "未知权限"
	}
}

// RequestPermission triggers the system authorization prompt where macOS
// offers one (only the first time; later calls just report the state) and
// returns the status observed immediately afterwards. The prompt itself is
// asynchronous, so callers should keep polling PermissionStatus.
func RequestPermission(p Permission) (CapabilityStatus, string) {
	n := loadDarwinNative()
	switch p {
	case PermissionScreenRecording:
		if n.requestScreen != nil {
			n.requestScreen()
		}
	case PermissionAccessibility:
		requestDarwinAccessibility(n)
	case PermissionMicrophone:
		requestDarwinMicrophone(n)
	}
	return PermissionStatus(p)
}

func requestDarwinAccessibility(n *darwinNative) {
	if n.axTrustedWithOptions == nil || n.cfDictionaryCreate == nil || n.axPromptKey == 0 || n.cfBooleanTrue == 0 {
		return
	}
	keys := [1]uintptr{n.axPromptKey}
	values := [1]uintptr{n.cfBooleanTrue}
	options := n.cfDictionaryCreate(0, unsafe.Pointer(&keys[0]), unsafe.Pointer(&values[0]), 1, n.dictKeyCallbacks, n.dictValueCallbacks)
	if options == 0 {
		return
	}
	n.axTrustedWithOptions(options)
	if n.cfRelease != nil {
		n.cfRelease(options)
	}
}

func requestDarwinMicrophone(n *darwinNative) {
	if n.avCaptureDevice == 0 || n.avMediaTypeAudio == 0 {
		return
	}
	if code := objc.Send[int64](n.avCaptureDevice, objc.RegisterName("authorizationStatusForMediaType:"), n.avMediaTypeAudio); code != 0 {
		return // already determined; macOS will not prompt again
	}
	// The result is observed by polling PermissionStatus. AVFoundation copies
	// the completion block, so our reference can be released right away.
	handler := objc.NewBlock(func(_ objc.Block, _ bool) {})
	n.avCaptureDevice.Send(objc.RegisterName("requestAccessForMediaType:completionHandler:"), n.avMediaTypeAudio, handler)
	handler.Release()
}

// OpenPermissionSettings opens the System Settings pane where the user can
// grant p.
func OpenPermissionSettings(p Permission) error {
	url := PermissionSettingsURL(p)
	if url == "" {
		return fmt.Errorf("%w: %s", ErrPermissionUnsupported, p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/bin/open", url).CombinedOutput()
	if err != nil {
		if text := strings.TrimSpace(string(output)); text != "" {
			return fmt.Errorf("platform: open %s: %w: %s", url, err, text)
		}
		return fmt.Errorf("platform: open %s: %w", url, err)
	}
	return nil
}

// PreferredLogDir returns ~/Library/Logs/MatrixClass on macOS. Writing next
// to the executable is not possible inside a signed .app bundle.
func PreferredLogDir() string { return LogDir() }

// PreferredDataDir returns ~/Library/Application Support/MatrixClass.
func PreferredDataDir() string { return ConfigDir() }
