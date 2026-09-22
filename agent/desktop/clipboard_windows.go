//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"relayproxy/internal/protocol"
)

var (
	clipboardKernel32DLL = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalSize       = clipboardKernel32DLL.NewProc("GlobalSize")
)

func openWindowsClipboard(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if win.OpenClipboard(0) {
			return nil
		}
		lastErr = errors.New("OpenClipboard failed")
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func globalMemorySize(handle win.HANDLE) uintptr {
	size, _, _ := procGlobalSize.Call(uintptr(handle))
	return size
}

func readWindowsClipboardText(ctx context.Context) (string, error) {
	if !win.IsClipboardFormatAvailable(win.CF_UNICODETEXT) {
		return "", ErrClipboardTextUnavailable
	}
	if err := openWindowsClipboard(ctx); err != nil {
		return "", err
	}
	defer win.CloseClipboard()

	handle := win.GetClipboardData(win.CF_UNICODETEXT)
	if handle == 0 {
		return "", errors.New("GetClipboardData(CF_UNICODETEXT) failed")
	}
	size := globalMemorySize(handle)
	if size < 2 {
		return "", nil
	}
	if size > uintptr(protocol.MaxDesktopClipboardBytes*2+2) {
		return "", fmt.Errorf("Windows clipboard text buffer is too large: %d bytes", size)
	}

	ptr := win.GlobalLock(win.HGLOBAL(handle))
	if ptr == nil {
		return "", errors.New("GlobalLock clipboard data failed")
	}
	defer win.GlobalUnlock(win.HGLOBAL(handle))

	units := unsafe.Slice((*uint16)(ptr), int(size/2))
	text := windows.UTF16ToString(units)
	return validateClipboardText(text)
}

func writeWindowsClipboardText(ctx context.Context, text string) error {
	text, err := validateClipboardText(text)
	if err != nil {
		return err
	}
	units, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	bytes := uintptr(len(units) * 2)
	memory := win.GlobalAlloc(win.GMEM_MOVEABLE|win.GMEM_ZEROINIT, bytes)
	if memory == 0 {
		return errors.New("GlobalAlloc clipboard data failed")
	}
	owned := true
	defer func() {
		if owned {
			win.GlobalFree(memory)
		}
	}()

	ptr := win.GlobalLock(memory)
	if ptr == nil {
		return errors.New("GlobalLock clipboard write buffer failed")
	}
	copy(unsafe.Slice((*uint16)(ptr), len(units)), units)
	win.GlobalUnlock(memory)

	if err := openWindowsClipboard(ctx); err != nil {
		return err
	}
	defer win.CloseClipboard()
	if !win.EmptyClipboard() {
		return errors.New("EmptyClipboard failed")
	}
	if win.SetClipboardData(win.CF_UNICODETEXT, win.HANDLE(memory)) == 0 {
		return errors.New("SetClipboardData(CF_UNICODETEXT) failed")
	}
	owned = false
	return nil
}

func (c *windowsCapture) ClipboardText(ctx context.Context) (string, error) {
	if c == nil {
		return "", errors.New("Windows desktop capture is unavailable")
	}
	return readWindowsClipboardText(ctx)
}

func (c *windowsCapture) SetClipboardText(ctx context.Context, text string) error {
	if c == nil {
		return errors.New("Windows desktop capture is unavailable")
	}
	return writeWindowsClipboardText(ctx, text)
}
