//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/go-mswin/screencapture"
	"github.com/lxn/win"

	"relayproxy/internal/protocol"
)

type windowsInputSink struct {
	mu      sync.Mutex
	keys    map[uint16]bool
	buttons map[string]bool
	lastX   uint16
	lastY   uint16

	displayMapped bool
	displayBounds screencapture.Rect
	virtualBounds screencapture.Rect
}

func newWindowsInputSink() *windowsInputSink {
	return &windowsInputSink{
		keys:    make(map[uint16]bool),
		buttons: make(map[string]bool),
	}
}

func virtualDesktopBounds() (screencapture.Rect, error) {
	bounds := screencapture.Rect{
		X: int(win.GetSystemMetrics(win.SM_XVIRTUALSCREEN)),
		Y: int(win.GetSystemMetrics(win.SM_YVIRTUALSCREEN)),
		W: int(win.GetSystemMetrics(win.SM_CXVIRTUALSCREEN)),
		H: int(win.GetSystemMetrics(win.SM_CYVIRTUALSCREEN)),
	}
	if bounds.W <= 0 || bounds.H <= 0 {
		return screencapture.Rect{}, errors.New("invalid Windows virtual desktop geometry")
	}
	return bounds, nil
}

func normalizedDisplayAxis(value uint16, displayOffset, displaySize, virtualSize int) uint16 {
	if displaySize <= 0 || virtualSize <= 0 {
		return value
	}
	displaySpan := displaySize - 1
	if displaySpan < 1 {
		displaySpan = 1
	}
	virtualSpan := virtualSize - 1
	if virtualSpan < 1 {
		virtualSpan = 1
	}
	pixel := displayOffset + int((int64(value)*int64(displaySpan)+32767)/65535)
	if pixel < 0 {
		pixel = 0
	}
	if pixel > virtualSpan {
		pixel = virtualSpan
	}
	mapped := (int64(pixel)*65535 + int64(virtualSpan)/2) / int64(virtualSpan)
	if mapped < 0 {
		return 0
	}
	if mapped > 65535 {
		return 65535
	}
	return uint16(mapped)
}

func mapDisplayNormalizedToVirtual(x, y uint16, display, virtual screencapture.Rect) (uint16, uint16) {
	return normalizedDisplayAxis(x, display.X-virtual.X, display.W, virtual.W),
		normalizedDisplayAxis(y, display.Y-virtual.Y, display.H, virtual.H)
}

func (s *windowsInputSink) BeginInputSession(ctx context.Context, cfg HostConfig) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.displayMapped = false
	s.displayBounds = screencapture.Rect{}
	s.virtualBounds = screencapture.Rect{}
	s.mu.Unlock()
	if cfg.DisplayID == "" {
		return nil
	}
	displays, err := screencapture.Displays(ctx)
	if err != nil {
		return fmt.Errorf("enumerate displays for input mapping: %w", err)
	}
	display, selected, err := resolveWindowsDisplay(displays, cfg.DisplayID)
	if err != nil {
		return err
	}
	if !selected || display.ID == 0 {
		return fmt.Errorf("Windows display %q cannot be mapped for input", cfg.DisplayID)
	}
	virtual, err := virtualDesktopBounds()
	if err != nil {
		return err
	}
	if display.Bounds.W <= 0 || display.Bounds.H <= 0 {
		return fmt.Errorf("Windows display %q has invalid geometry", cfg.DisplayID)
	}

	s.mu.Lock()
	s.displayMapped = true
	s.displayBounds = display.Bounds
	s.virtualBounds = virtual
	s.mu.Unlock()
	return nil
}

func (s *windowsInputSink) EndInputSession() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.displayMapped = false
	s.displayBounds = screencapture.Rect{}
	s.virtualBounds = screencapture.Rect{}
	s.mu.Unlock()
	return nil
}

func (s *windowsInputSink) mapMousePoint(x, y uint16) (uint16, uint16) {
	if !s.displayMapped {
		return x, y
	}
	return mapDisplayNormalizedToVirtual(x, y, s.displayBounds, s.virtualBounds)
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
		if down {
			return win.MOUSEEVENTF_LEFTDOWN, 0, true
		}
		return win.MOUSEEVENTF_LEFTUP, 0, true
	case protocol.DesktopMouseButtonRight:
		if down {
			return win.MOUSEEVENTF_RIGHTDOWN, 0, true
		}
		return win.MOUSEEVENTF_RIGHTUP, 0, true
	case protocol.DesktopMouseButtonMiddle:
		if down {
			return win.MOUSEEVENTF_MIDDLEDOWN, 0, true
		}
		return win.MOUSEEVENTF_MIDDLEUP, 0, true
	case protocol.DesktopMouseButtonX1:
		if down {
			return win.MOUSEEVENTF_XDOWN, win.XBUTTON1, true
		}
		return win.MOUSEEVENTF_XUP, win.XBUTTON1, true
	case protocol.DesktopMouseButtonX2:
		if down {
			return win.MOUSEEVENTF_XDOWN, win.XBUTTON2, true
		}
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
		x, y := s.mapMousePoint(event.X, event.Y)
		s.lastX, s.lastY = x, y
		return sendMouseInput(x, y, win.MOUSEEVENTF_MOVE|win.MOUSEEVENTF_MOVE_NOCOALESCE, 0)
	case protocol.DesktopInputMouseDown, protocol.DesktopInputMouseUp:
		x, y := s.mapMousePoint(event.X, event.Y)
		s.lastX, s.lastY = x, y
		down := event.Kind == protocol.DesktopInputMouseDown
		flags, data, ok := mouseButtonFlags(event.Button, down)
		if !ok {
			return errors.New("unsupported Windows mouse button")
		}
		if err := sendMouseInput(x, y, win.MOUSEEVENTF_MOVE|flags, data); err != nil {
			return err
		}
		if down {
			s.buttons[event.Button] = true
		} else {
			delete(s.buttons, event.Button)
		}
	case protocol.DesktopInputMouseWheel:
		x, y := s.mapMousePoint(event.X, event.Y)
		s.lastX, s.lastY = x, y
		flags := uint32(win.MOUSEEVENTF_WHEEL)
		if event.Horizontal {
			flags = win.MOUSEEVENTF_HWHEEL
		}
		return sendMouseInput(x, y, win.MOUSEEVENTF_MOVE|flags, uint32(event.WheelDelta))
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
