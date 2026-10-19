//go:build windows

package hostalias

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows runs the hosts editor elevated without spawning powershell.exe or
// showing the console of this console-subsystem helper. The UAC consent prompt
// stays visible and requires explicit user approval.
type shellExecuteInfo struct {
	Size       uint32
	Mask       uint32
	Hwnd       uintptr
	Verb       *uint16
	File       *uint16
	Parameters *uint16
	Directory  *uint16
	Show       int32
	Instance   uintptr
	IDList     uintptr
	Class      *uint16
	KeyClass   uintptr
	HotKey     uint32
	Icon       uintptr
	Process    windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x40
	seeMaskNoAsync        = 0x100
	swHide                = 0
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func elevateWindows(exe, arg string) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(arg)
	if err != nil {
		return err
	}
	info := shellExecuteInfo{
		Size:       uint32(unsafe.Sizeof(shellExecuteInfo{})),
		Mask:       seeMaskNoCloseProcess | seeMaskNoAsync,
		Verb:       verb,
		File:       file,
		Parameters: params,
		Show:       swHide,
	}
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return fmt.Errorf("Windows UAC declined or failed: %w", callErr)
	}
	if info.Process == 0 {
		return fmt.Errorf("Windows UAC helper did not return a process handle")
	}
	defer windows.CloseHandle(info.Process)
	wait, err := windows.WaitForSingleObject(info.Process, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for elevated RDP hosts helper: %w", err)
	}
	if wait != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("unexpected RDP hosts helper wait status %d", wait)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.Process, &code); err != nil {
		return fmt.Errorf("read elevated RDP hosts helper exit status: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("elevated RDP hosts helper exited with code %d", code)
	}
	return nil
}
