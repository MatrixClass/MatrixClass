//go:build windows

package control

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmSysKeyDown = 0x0104
	wmQuit       = 0x0012

	llkhfAltDown = 0x20

	vkTab     = 0x09
	vkEscape  = 0x1B
	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkF4      = 0x73
	vkControl = 0x11
)

var (
	procSetWindowsHookExW   = user32DLL.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32DLL.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32DLL.NewProc("CallNextHookEx")
	procGetMessageW         = user32DLL.NewProc("GetMessageW")
	procPostThreadMessageW  = user32DLL.NewProc("PostThreadMessageW")
	procGetKeyState         = user32DLL.NewProc("GetKeyState")
)

type kbdllHookStruct struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type win32Msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}
type WindowsLockBackend struct {
	mu       sync.Mutex
	active   bool
	hook     uintptr
	threadID uint32
	doneCh   chan struct{}
	overlays OverlayHooks
}

func newPlatformLockBackend(overlays OverlayHooks) LockBackend {
	return &WindowsLockBackend{overlays: overlays}
}

func NewWindowsLockBackend(overlays OverlayHooks) *WindowsLockBackend {
	return &WindowsLockBackend{overlays: overlays}
}

func (b *WindowsLockBackend) SetOverlayHooks(overlays OverlayHooks) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.overlays = overlays
}

func (b *WindowsLockBackend) EnableLock(message string) error {
	b.mu.Lock()
	if b.active {
		overlays := b.overlays
		b.mu.Unlock()
		if overlays.OnUpdate != nil {
			overlays.OnUpdate(message)
		}
		return nil
	}

	readyCh := make(chan error, 1)
	doneCh := make(chan struct{})
	b.doneCh = doneCh
	b.active = true
	overlays := b.overlays
	b.mu.Unlock()

	go b.runHookLoop(readyCh, doneCh)

	if err := <-readyCh; err != nil {
		b.mu.Lock()
		b.active = false
		b.doneCh = nil
		b.mu.Unlock()
		return err
	}

	if overlays.OnShow != nil {
		overlays.OnShow(message)
	}
	return nil
}

func (b *WindowsLockBackend) UpdateMessage(message string) error {
	b.mu.Lock()
	overlays := b.overlays
	b.mu.Unlock()
	if overlays.OnUpdate != nil {
		overlays.OnUpdate(message)
	}
	return nil
}

func (b *WindowsLockBackend) DisableLock() error {
	b.mu.Lock()
	if !b.active {
		b.mu.Unlock()
		return nil
	}
	tid := b.threadID
	doneCh := b.doneCh
	overlays := b.overlays
	b.active = false
	b.doneCh = nil
	b.mu.Unlock()

	if tid != 0 {
		_, _, _ = procPostThreadMessageW.Call(uintptr(tid), wmQuit, 0, 0)
	}
	if doneCh != nil {
		<-doneCh
	}
	if overlays.OnHide != nil {
		overlays.OnHide()
	}
	return nil
}

func (b *WindowsLockBackend) runHookLoop(readyCh chan<- error, doneCh chan<- struct{}) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(doneCh)

	tid := windows.GetCurrentThreadId()
	cb := syscall.NewCallback(func(nCode int32, wParam uintptr, lParam uintptr) uintptr {
		if nCode >= 0 && shouldBlockLockedKey(wParam, lParam) {
			return 1
		}
		ret, _, _ := procCallNextHookEx.Call(0, uintptr(nCode), wParam, lParam)
		return ret
	})

	hook, _, err := procSetWindowsHookExW.Call(whKeyboardLL, cb, 0, 0)
	if hook == 0 {
		readyCh <- fmt.Errorf("control: SetWindowsHookExW failed: %w", err)
		return
	}

	b.mu.Lock()
	b.hook = hook
	b.threadID = tid
	b.mu.Unlock()

	readyCh <- nil

	var msg win32Msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
	}

	_, _, _ = procUnhookWindowsHookEx.Call(hook)
	b.mu.Lock()
	b.hook = 0
	b.threadID = 0
	b.mu.Unlock()
}

func shouldBlockLockedKey(wParam uintptr, lParam uintptr) bool {
	if lParam == 0 {
		return false
	}
	if wParam != wmKeyDown && wParam != wmSysKeyDown {
		return false
	}
	kb := (*kbdllHookStruct)(unsafe.Pointer(lParam))
	altDown := (kb.flags & llkhfAltDown) != 0
	switch kb.vkCode {
	case vkLWin, vkRWin:
		return true
	case vkTab:
		return altDown
	case vkF4:
		return altDown
	case vkEscape:
		if altDown {
			return true
		}
		ctrlState, _, _ := procGetKeyState.Call(vkControl)
		return (int16(ctrlState) & -32768) != 0
	default:
		return false
	}
}
