//go:build windows

package control

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessInspector struct{}

func newPlatformProcessInspector() ProcessInspector {
	return &windowsProcessInspector{}
}

func (w *windowsProcessInspector) ListProcesses() ([]ProcessInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("control: CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, fmt.Errorf("control: Process32First: %w", err)
	}

	var procs []ProcessInfo
	for {
		procs = append(procs, ProcessInfo{
			PID:  entry.ProcessID,
			Name: windows.UTF16ToString(entry.ExeFile[:]),
		})
		if err := windows.Process32Next(snap, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return procs, fmt.Errorf("control: Process32Next: %w", err)
		}
	}
	return procs, nil
}

func (w *windowsProcessInspector) TerminateProcess(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("control: OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("control: TerminateProcess(%d): %w", pid, err)
	}
	return nil
}

// platformProtectedProcesses are Windows session/system processes that policy
// enforcement (especially whitelist mode) must never terminate.
var platformProtectedProcesses = []string{
	"system",
	"idle",
	"registry",
	"memory compression",
	"secure system",
	"smss.exe",
	"csrss.exe",
	"wininit.exe",
	"winlogon.exe",
	"logonui.exe",
	"userinit.exe",
	"services.exe",
	"lsass.exe",
	"lsaiso.exe",
	"svchost.exe",
	"explorer.exe",
	"dwm.exe",
	"conhost.exe",
	"taskmgr.exe",
	"sihost.exe",
	"ctfmon.exe",
	"fontdrvhost.exe",
	"taskhostw.exe",
	"runtimebroker.exe",
	"dllhost.exe",
	"audiodg.exe",
	"spoolsv.exe",
	"wudfhost.exe",
	"dashost.exe",
	"lockapp.exe",
	"startmenuexperiencehost.exe",
	"shellexperiencehost.exe",
	"searchhost.exe",
	"searchapp.exe",
	"searchui.exe",
	"searchindexer.exe",
	"textinputhost.exe",
	"applicationframehost.exe",
	"systemsettingsbroker.exe",
	"securityhealthservice.exe",
	"securityhealthsystray.exe",
	"smartscreen.exe",
	"msmpeng.exe",
	"nissrv.exe",
	"msedgewebview2.exe",
	"wmiprvse.exe",
}
