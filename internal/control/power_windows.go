//go:build windows

package control

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ewxLogoff          uint32 = 0x00000000
	ewxForceIfHung     uint32 = 0x00000010
	shtdnReasonFlagUse uint32 = 0x80000000
)

var (
	advapi32DLL                 = windows.NewLazySystemDLL("advapi32.dll")
	procExitWindowsEx           = user32DLL.NewProc("ExitWindowsEx")
	procInitiateSystemShutdownW = advapi32DLL.NewProc("InitiateSystemShutdownExW")
)

type windowsPowerExecutor struct{}

func newPlatformPowerExecutor() PowerExecutor {
	return &windowsPowerExecutor{}
}

func (w *windowsPowerExecutor) Shutdown(timeoutSec uint32, reason string) error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}
	return initiateShutdown(timeoutSec, reason, false)
}

func (w *windowsPowerExecutor) Reboot(timeoutSec uint32, reason string) error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}
	return initiateShutdown(timeoutSec, reason, true)
}

func (w *windowsPowerExecutor) Logoff() error {
	ret, _, err := procExitWindowsEx.Call(
		uintptr(ewxLogoff|ewxForceIfHung),
		0,
	)
	if ret == 0 {
		return fmt.Errorf("control: ExitWindowsEx failed: %w", err)
	}
	return nil
}

func initiateShutdown(timeoutSec uint32, reason string, reboot bool) error {
	var msgPtr *uint16
	if reason != "" {
		p, err := windows.UTF16PtrFromString(reason)
		if err != nil {
			return err
		}
		msgPtr = p
	}
	var rebootFlag uintptr
	if reboot {
		rebootFlag = 1
	}
	ret, _, err := procInitiateSystemShutdownW.Call(
		0,
		uintptr(unsafe.Pointer(msgPtr)),
		uintptr(timeoutSec),
		1,
		rebootFlag,
		uintptr(shtdnReasonFlagUse),
	)
	if ret == 0 {
		return fmt.Errorf("control: InitiateSystemShutdownExW failed: %w", err)
	}
	return nil
}

func enableShutdownPrivilege() error {
	var token windows.Token
	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY,
		&token,
	)
	if err != nil {
		return fmt.Errorf("control: OpenProcessToken: %w", err)
	}
	defer token.Close()

	privName, err := windows.UTF16PtrFromString("SeShutdownPrivilege")
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, privName, &luid); err != nil {
		return fmt.Errorf("control: LookupPrivilegeValue: %w", err)
	}
	tp := windows.Tokenprivileges{
		PrivilegeCount: 1,
		Privileges: [1]windows.LUIDAndAttributes{
			{
				Luid:       luid,
				Attributes: windows.SE_PRIVILEGE_ENABLED,
			},
		},
	}
	if err := windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil); err != nil {
		return fmt.Errorf("control: AdjustTokenPrivileges: %w", err)
	}
	return nil
}
