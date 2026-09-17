//go:build windows

package main

import (
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

var (
	consoleKernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = consoleKernel32.NewProc("GetConsoleProcessList")
)

// detachConsole hides the console window that Windows attached to this
// console-subsystem process.
//
// The desktop entry point ships as relay-agent-gui.exe, which is linked with
// -H=windowsgui and therefore never receives a console in the first place. This
// exists purely as a safety net for the other binary: a user who double-clicks
// relay-agent.exe would otherwise sit in front of a stray black console window
// for the entire session, which is exactly the behaviour the desktop rewrite
// set out to remove.
func detachConsole() {
	hwnd := win.GetConsoleWindow()
	if hwnd == 0 || !ownsConsole() {
		return
	}
	win.ShowWindow(hwnd, win.SW_HIDE)
}

// ownsConsole reports whether this process is the only console client. A
// console launched by Explorer or the login startup entry is private to the
// agent; a console inherited from cmd/PowerShell is shared and must remain
// visible to the caller.
func ownsConsole() bool {
	var processIDs [64]uint32
	count, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&processIDs[0])),
		uintptr(len(processIDs)),
	)
	return count == 1
}
