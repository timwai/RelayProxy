//go:build windows

package singleton

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32        = syscall.NewLazyDLL("kernel32.dll")
	procCreateMutex = kernel32.NewProc("CreateMutexW")
	procCloseHandle = kernel32.NewProc("CloseHandle")
)

type Mutex struct {
	handle syscall.Handle
}

// Acquire attempts to acquire a named mutex across the Windows session
func Acquire(name string) (*Mutex, error) {
	namePtr, err := syscall.UTF16PtrFromString("Global\\" + name)
	if err != nil {
		return nil, err
	}

	ret, _, callErr := procCreateMutex.Call(
		0,
		1, // bInitialOwner = TRUE
		uintptr(unsafe.Pointer(namePtr)),
	)

	handle := syscall.Handle(ret)
	if handle == 0 {
		return nil, fmt.Errorf("failed to create mutex: %v", callErr)
	}

	// ERROR_ALREADY_EXISTS = 183
	if callErr == syscall.Errno(183) {
		_, _, _ = procCloseHandle.Call(uintptr(handle))
		return nil, fmt.Errorf("another instance of %s is already running", name)
	}

	return &Mutex{handle: handle}, nil
}

func (m *Mutex) Release() {
	if m.handle != 0 {
		_, _, _ = procCloseHandle.Call(uintptr(m.handle))
		m.handle = 0
	}
}
