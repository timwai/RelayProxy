//go:build windows

package desktop

import (
	"context"
	"errors"
	"sync"
	"unsafe"

	"github.com/lxn/win"

	"relayproxy/internal/protocol"
)

type windowsInputSink struct {
	mu      sync.Mutex
	keys    map[uint16]bool
	buttons map[string]bool
	lastX   uint16
	lastY   uint16
}

func newWindowsInputSink() *windowsInputSink {
	return &windowsInputSink{
		keys:    make(map[uint16]bool),
		buttons: make(map[string]bool),
	}
}

func sendKeyboardInput(vk uint16, flags uint32) error {
	input := win.KEYBD_INPUT{
		Type: win.INPUT_KEYBOARD,
		Ki:   win.KEYBDINPUT{WVk: vk, DwFlags: flags},
	}
	if win.SendInput(1, unsafe.Pointer(&input), int32(unsafe.Sizeof(input))) != 1 {
		return errors.New("Windows SendInput keyboard injection was rejected")
	}
	return nil
}

func mouseButtonFlags(button string, down bool) (flags uint32, data uint32, ok bool) {
	switch button {
	case protocol.DesktopMouseButtonLeft:
		if down { return win.MOUSEEVENTF_LEFTDOWN, 0, true }
		return win.MOUSEEVENTF_LEFTUP, 0, true
	case protocol.DesktopMouseButtonRight:
		if down { return win.MOUSEEVENTF_RIGHTDOWN, 0, true }
		return win.MOUSEEVENTF_RIGHTUP, 0, true
	case protocol.DesktopMouseButtonMiddle:
		if down { return win.MOUSEEVENTF_MIDDLEDOWN, 0, true }
		return win.MOUSEEVENTF_MIDDLEUP, 0, true
	case protocol.DesktopMouseButtonX1:
		if down { return win.MOUSEEVENTF_XDOWN, win.XBUTTON1, true }
		return win.MOUSEEVENTF_XUP, win.XBUTTON1, true
	case protocol.DesktopMouseButtonX2:
		if down { return win.MOUSEEVENTF_XDOWN, win.XBUTTON2, true }
		return win.MOUSEEVENTF_XUP, win.XBUTTON2, true
	default:
		return 0, 0, false
	}
}

func sendMouseInput(x, y uint16, flags, data uint32) error {
	input := win.MOUSE_INPUT{
		Type: win.INPUT_MOUSE,
		Mi: win.MOUSEINPUT{
			Dx:        int32(x),
			Dy:        int32(y),
			MouseData: data,
			DwFlags:   flags | win.MOUSEEVENTF_ABSOLUTE | win.MOUSEEVENTF_VIRTUALDESK,
		},
	}
	if win.SendInput(1, unsafe.Pointer(&input), int32(unsafe.Sizeof(input))) != 1 {
		return errors.New("Windows SendInput mouse injection was rejected")
	}
	return nil
}

func (s *windowsInputSink) ApplyInput(ctx context.Context, event protocol.DesktopInputEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateDesktopInputEvent(event); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastX, s.lastY = event.X, event.Y
	switch event.Kind {
	case protocol.DesktopInputKeyDown, protocol.DesktopInputKeyUp:
		flags := uint32(0)
		if event.Extended {
			flags |= win.KEYEVENTF_EXTENDEDKEY
		}
		if event.Kind == protocol.DesktopInputKeyUp {
			flags |= win.KEYEVENTF_KEYUP
		}
		if err := sendKeyboardInput(event.VirtualKey, flags); err != nil {
			return err
		}
		if event.Kind == protocol.DesktopInputKeyDown {
			s.keys[event.VirtualKey] = event.Extended
		} else {
			delete(s.keys, event.VirtualKey)
		}
	case protocol.DesktopInputMouseMove:
		return sendMouseInput(event.X, event.Y, win.MOUSEEVENTF_MOVE|win.MOUSEEVENTF_MOVE_NOCOALESCE, 0)
	case protocol.DesktopInputMouseDown, protocol.DesktopInputMouseUp:
		down := event.Kind == protocol.DesktopInputMouseDown
		flags, data, ok := mouseButtonFlags(event.Button, down)
		if !ok {
			return errors.New("unsupported Windows mouse button")
		}
		if err := sendMouseInput(event.X, event.Y, win.MOUSEEVENTF_MOVE|flags, data); err != nil {
			return err
		}
		if down {
			s.buttons[event.Button] = true
		} else {
			delete(s.buttons, event.Button)
		}
	case protocol.DesktopInputMouseWheel:
		flags := uint32(win.MOUSEEVENTF_WHEEL)
		if event.Horizontal {
			flags = win.MOUSEEVENTF_HWHEEL
		}
		return sendMouseInput(event.X, event.Y, win.MOUSEEVENTF_MOVE|flags, uint32(event.WheelDelta))
	}
	return nil
}

func (s *windowsInputSink) ReleaseAll() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for vk, extended := range s.keys {
		flags := uint32(win.KEYEVENTF_KEYUP)
		if extended {
			flags |= win.KEYEVENTF_EXTENDEDKEY
		}
		if err := sendKeyboardInput(vk, flags); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for button := range s.buttons {
		flags, data, ok := mouseButtonFlags(button, false)
		if !ok {
			continue
		}
		if err := sendMouseInput(s.lastX, s.lastY, win.MOUSEEVENTF_MOVE|flags, data); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	clear(s.keys)
	clear(s.buttons)
	return firstErr
}
