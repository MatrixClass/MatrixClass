//go:build windows

package control

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32DLL     = windows.NewLazySystemDLL("user32.dll")
	procSendInput = user32DLL.NewProc("SendInput")
)

type win32RawMouseInput struct {
	dx          int32
	dy          int32
	mouseData   uint32
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type win32RawKeybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
	_pad        [8]byte
}

type win32RawInput struct {
	inputType uint32
	_pad      [4]byte
	union     [32]byte
}

func init() {
	var raw win32RawInput
	if unsafe.Sizeof(raw) != 40 {
		panic(fmt.Sprintf("unexpected INPUT struct size: %d", unsafe.Sizeof(raw)))
	}
}

type windowsInputBackend struct{}

func newPlatformInputBackend() InputBackend {
	return &windowsInputBackend{}
}

func toRawWin32Input(in WinInput) win32RawInput {
	var raw win32RawInput
	raw.inputType = in.Type
	switch in.Type {
	case InputTypeMouse:
		mi := (*win32RawMouseInput)(unsafe.Pointer(&raw.union[0]))
		mi.dx = in.Mi.Dx
		mi.dy = in.Mi.Dy
		mi.mouseData = in.Mi.MouseData
		mi.dwFlags = in.Mi.DwFlags
		mi.time = in.Mi.Time
		mi.dwExtraInfo = in.Mi.DwExtraInfo
	case InputTypeKeyboard:
		ki := (*win32RawKeybdInput)(unsafe.Pointer(&raw.union[0]))
		ki.wVk = in.Ki.WVk
		ki.wScan = in.Ki.WScan
		ki.dwFlags = in.Ki.DwFlags
		ki.time = in.Ki.Time
		ki.dwExtraInfo = in.Ki.DwExtraInfo
	}
	return raw
}

func (w *windowsInputBackend) SendInputs(inputs []WinInput) error {
	if len(inputs) == 0 {
		return nil
	}
	raw := make([]win32RawInput, len(inputs))
	for i, in := range inputs {
		raw[i] = toRawWin32Input(in)
	}
	ret, _, err := procSendInput.Call(
		uintptr(len(raw)),
		uintptr(unsafe.Pointer(&raw[0])),
		unsafe.Sizeof(raw[0]),
	)
	if int(ret) != len(raw) {
		if err != nil && err != windows.ERROR_SUCCESS {
			return fmt.Errorf("control: SendInput sent %d/%d events: %w", ret, len(raw), err)
		}
		return fmt.Errorf("control: SendInput sent %d/%d events", ret, len(raw))
	}
	return nil
}
