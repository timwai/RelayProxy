//go:build windows

package viewer

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"relayproxy/internal/protocol"
)

const (
	nativeViewerClass = "RelayProxyNativeViewerWindow"

	wmNativeViewerFrame = win.WM_APP + 0x311
	wmMouseHWheel       = 0x020e
)

var (
	nativeViewerClassOnce sync.Once
	nativeViewerClassErr  error
	nativeViewerClassName *uint16
	nativeViewerWndProc   = windows.NewCallback(nativeViewerWindowProc)
	nativeViewerWindows   sync.Map
)

type windowsViewer struct {
	config Config

	hwnd     atomic.Uintptr
	viewport atomic.Uint64

	frameMu     sync.Mutex
	latest      Frame
	latestD3D11 D3D11Frame
	latestIsGPU bool
	cursor      CursorOverlay

	renderer *d3d11Renderer

	inputSequence  uint64
	pressedKeys    map[uint16]bool
	pressedButtons map[string]bool

	errMu sync.Mutex
	err   error

	closeOnce sync.Once
	done      chan struct{}
}

func registerNativeViewerClass() error {
	nativeViewerClassOnce.Do(func() {
		var err error
		nativeViewerClassName, err = windows.UTF16PtrFromString(nativeViewerClass)
		if err != nil {
			nativeViewerClassErr = err
			return
		}
		instance := win.GetModuleHandle(nil)
		wc := win.WNDCLASSEX{
			CbSize:        uint32(unsafe.Sizeof(win.WNDCLASSEX{})),
			Style:         win.CS_HREDRAW | win.CS_VREDRAW,
			LpfnWndProc:   nativeViewerWndProc,
			HInstance:     instance,
			LpszClassName: nativeViewerClassName,
		}
		if atom := win.RegisterClassEx(&wc); atom == 0 {
			nativeViewerClassErr = fmt.Errorf("RegisterClassEx(%s) failed: %w", nativeViewerClass, windows.GetLastError())
		}
	})
	return nativeViewerClassErr
}

func Open(config Config) (Native, error) {
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: invalid native viewer dimensions", ErrUnavailable)
	}
	if config.Title == "" {
		config.Title = "RelayProxy Remote Desktop"
	}
	viewer := &windowsViewer{
		config:         config,
		done:           make(chan struct{}),
		pressedKeys:    make(map[uint16]bool),
		pressedButtons: make(map[string]bool),
	}
	initCh := make(chan error, 1)
	go viewer.run(initCh)
	if err := <-initCh; err != nil {
		<-viewer.done
		return nil, err
	}
	return viewer, nil
}

func (v *windowsViewer) run(initCh chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(v.done)

	if err := registerNativeViewerClass(); err != nil {
		initCh <- err
		return
	}
	title, err := windows.UTF16PtrFromString(v.config.Title)
	if err != nil {
		initCh <- err
		return
	}

	style := uint32(win.WS_OVERLAPPED | win.WS_CAPTION | win.WS_SYSMENU | win.WS_MINIMIZEBOX | win.WS_MAXIMIZEBOX | win.WS_THICKFRAME)
	clientWidth, clientHeight := v.config.ViewportWidth, v.config.ViewportHeight
	if clientWidth <= 0 {
		clientWidth = v.config.Width
	}
	if clientHeight <= 0 {
		clientHeight = v.config.Height
	}
	rect := win.RECT{Left: 0, Top: 0, Right: int32(clientWidth), Bottom: int32(clientHeight)}
	if !win.AdjustWindowRect(&rect, style, false) {
		initCh <- fmt.Errorf("AdjustWindowRect failed: %w", windows.GetLastError())
		return
	}
	instance := win.GetModuleHandle(nil)
	hwnd := win.CreateWindowEx(
		0,
		nativeViewerClassName,
		title,
		style,
		int32(win.CW_USEDEFAULT),
		int32(win.CW_USEDEFAULT),
		rect.Right-rect.Left,
		rect.Bottom-rect.Top,
		0,
		0,
		instance,
		nil,
	)
	if hwnd == 0 {
		initCh <- fmt.Errorf("CreateWindowEx native viewer failed: %w", windows.GetLastError())
		return
	}
	v.hwnd.Store(uintptr(hwnd))
	var clientRect win.RECT
	if win.GetClientRect(hwnd, &clientRect) {
		v.updateViewport(int(clientRect.Right-clientRect.Left), int(clientRect.Bottom-clientRect.Top), false)
	} else {
		v.updateViewport(clientWidth, clientHeight, false)
	}

	renderer, err := newD3D11Renderer(hwnd, v.config.Width, v.config.Height)
	if err != nil {
		v.hwnd.Store(0)
		win.DestroyWindow(hwnd)
		initCh <- err
		return
	}
	v.renderer = renderer
	nativeViewerWindows.Store(uintptr(hwnd), v)
	defer func() {
		nativeViewerWindows.Delete(uintptr(hwnd))
		v.releaseLatestD3D11()
		renderer.Close()
		v.renderer = nil
		v.hwnd.Store(0)
	}()

	win.ShowWindow(hwnd, win.SW_SHOW)
	win.UpdateWindow(hwnd)
	win.SetForegroundWindow(hwnd)
	v.notifyViewport()
	initCh <- nil

	var msg win.MSG
	for {
		result := int32(win.GetMessage(&msg, 0, 0, 0))
		if result == 0 {
			return
		}
		if result < 0 {
			v.setError(fmt.Errorf("GetMessage native viewer failed: %w", windows.GetLastError()))
			return
		}
		win.TranslateMessage(&msg)
		win.DispatchMessage(&msg)
	}
}

func nativeViewerWindowProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	viewer, _ := nativeViewerFor(hwnd)
	switch msg {
	case win.WM_KEYDOWN, win.WM_SYSKEYDOWN:
		if viewer != nil {
			key := uint16(wParam)
			viewer.pressedKeys[key] = true
			viewer.emitInput(protocol.DesktopInputEvent{
				Kind:       protocol.DesktopInputKeyDown,
				VirtualKey: key,
				Extended:   lParam&(1<<24) != 0,
			})
		}
		return 0

	case win.WM_KEYUP, win.WM_SYSKEYUP:
		if viewer != nil {
			key := uint16(wParam)
			delete(viewer.pressedKeys, key)
			viewer.emitInput(protocol.DesktopInputEvent{
				Kind:       protocol.DesktopInputKeyUp,
				VirtualKey: key,
				Extended:   lParam&(1<<24) != 0,
			})
		}
		return 0

	case win.WM_MOUSEMOVE:
		if viewer != nil {
			x, y := clientPoint(lParam)
			nx, ny := viewer.normalizedPointer(x, y)
			viewer.emitInput(protocol.DesktopInputEvent{
				Kind: protocol.DesktopInputMouseMove,
				X:    nx,
				Y:    ny,
			})
		}
		return 0

	case win.WM_LBUTTONDOWN, win.WM_RBUTTONDOWN, win.WM_MBUTTONDOWN, win.WM_XBUTTONDOWN:
		if viewer != nil {
			button := mouseButton(msg, wParam)
			if button != "" {
				viewer.pressedButtons[button] = true
				win.SetFocus(hwnd)
				win.SetCapture(hwnd)
				viewer.emitInput(protocol.DesktopInputEvent{
					Kind:   protocol.DesktopInputMouseDown,
					Button: button,
				})
			}
		}
		return 0

	case win.WM_LBUTTONUP, win.WM_RBUTTONUP, win.WM_MBUTTONUP, win.WM_XBUTTONUP:
		if viewer != nil {
			button := mouseButton(msg, wParam)
			if button != "" {
				delete(viewer.pressedButtons, button)
				viewer.emitInput(protocol.DesktopInputEvent{
					Kind:   protocol.DesktopInputMouseUp,
					Button: button,
				})
				if len(viewer.pressedButtons) == 0 {
					win.ReleaseCapture()
				}
			}
		}
		return 0

	case win.WM_MOUSEWHEEL, wmMouseHWheel:
		if viewer != nil {
			viewer.emitInput(protocol.DesktopInputEvent{
				Kind:       protocol.DesktopInputMouseWheel,
				WheelDelta: int32(int16(uint16(wParam >> 16))),
				Horizontal: msg == wmMouseHWheel,
			})
		}
		return 0

	case win.WM_KILLFOCUS:
		if viewer != nil {
			viewer.releasePressedInput()
		}
		return 0

	case win.WM_SIZE:
		if viewer != nil {
			width := int(uint16(lParam & 0xffff))
			height := int(uint16((lParam >> 16) & 0xffff))
			viewer.updateViewport(width, height, true)
		}
		return 0

	case wmNativeViewerFrame:
		if viewer != nil {
			if err := viewer.renderLatest(); err != nil {
				viewer.setError(err)
				win.DestroyWindow(hwnd)
			}
		}
		return 0

	case win.WM_SETCURSOR:
		win.SetCursor(0)
		return 1

	case win.WM_ERASEBKGND:
		return 1

	case win.WM_CLOSE:
		if viewer != nil {
			viewer.releasePressedInput()
		}
		win.DestroyWindow(hwnd)
		return 0

	case win.WM_DESTROY:
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func nativeViewerFor(hwnd win.HWND) (*windowsViewer, bool) {
	value, ok := nativeViewerWindows.Load(uintptr(hwnd))
	if !ok {
		return nil, false
	}
	viewer, ok := value.(*windowsViewer)
	return viewer, ok
}

func clientPoint(lParam uintptr) (int, int) {
	x := int(int16(uint16(lParam & 0xffff)))
	y := int(int16(uint16((lParam >> 16) & 0xffff)))
	return x, y
}

func packViewport(width, height int) uint64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	return uint64(uint32(width))<<32 | uint64(uint32(height))
}

func unpackViewport(value uint64) Viewport {
	if value == 0 {
		return Viewport{}
	}
	return Viewport{
		Width:  int(uint32(value >> 32)),
		Height: int(uint32(value)),
	}
}

func (v *windowsViewer) updateViewport(width, height int, notify bool) {
	if v == nil || width <= 0 || height <= 0 {
		return
	}
	next := packViewport(width, height)
	if next == 0 || v.viewport.Swap(next) == next {
		return
	}
	if notify {
		v.notifyViewport()
	}
}

func (v *windowsViewer) notifyViewport() {
	if v == nil || v.config.OnViewport == nil {
		return
	}
	viewport := v.Viewport()
	if viewport.Valid() {
		v.config.OnViewport(viewport)
	}
}

func (v *windowsViewer) normalizedPointer(x, y int) (uint16, uint16) {
	viewport := v.Viewport()
	width, height := viewport.Width, viewport.Height
	if width <= 1 || height <= 1 {
		width, height = v.config.Width, v.config.Height
	}
	if width <= 1 || height <= 1 {
		return 0, 0
	}
	if x < 0 {
		x = 0
	} else if x >= width {
		x = width - 1
	}
	if y < 0 {
		y = 0
	} else if y >= height {
		y = height - 1
	}
	return uint16(x * 65535 / (width - 1)), uint16(y * 65535 / (height - 1))
}

func mouseButton(msg uint32, wParam uintptr) string {
	switch msg {
	case win.WM_LBUTTONDOWN, win.WM_LBUTTONUP:
		return protocol.DesktopMouseButtonLeft
	case win.WM_RBUTTONDOWN, win.WM_RBUTTONUP:
		return protocol.DesktopMouseButtonRight
	case win.WM_MBUTTONDOWN, win.WM_MBUTTONUP:
		return protocol.DesktopMouseButtonMiddle
	case win.WM_XBUTTONDOWN, win.WM_XBUTTONUP:
		if uint16(wParam>>16) == 1 {
			return protocol.DesktopMouseButtonX1
		}
		if uint16(wParam>>16) == 2 {
			return protocol.DesktopMouseButtonX2
		}
	}
	return ""
}

func (v *windowsViewer) emitInput(event protocol.DesktopInputEvent) {
	if v == nil || v.config.OnInput == nil {
		return
	}
	v.inputSequence++
	event.Sequence = v.inputSequence
	v.config.OnInput(event)
}

func (v *windowsViewer) releasePressedInput() {
	for key := range v.pressedKeys {
		v.emitInput(protocol.DesktopInputEvent{
			Kind:       protocol.DesktopInputKeyUp,
			VirtualKey: key,
		})
	}
	clear(v.pressedKeys)
	for button := range v.pressedButtons {
		v.emitInput(protocol.DesktopInputEvent{
			Kind:   protocol.DesktopInputMouseUp,
			Button: button,
		})
	}
	clear(v.pressedButtons)
	win.ReleaseCapture()
}

func (v *windowsViewer) renderLatest() error {
	v.frameMu.Lock()
	isGPU := v.latestIsGPU
	gpuFrame := v.latestD3D11
	cpuFrame := v.latest
	cursor := v.cursor
	if isGPU && gpuFrame.Resource != 0 {
		retainCOM(unsafe.Pointer(gpuFrame.Resource))
	}
	v.frameMu.Unlock()

	if v.renderer == nil {
		if isGPU && gpuFrame.Resource != 0 {
			releaseCOM(unsafe.Pointer(gpuFrame.Resource))
		}
		return ErrUnavailable
	}
	if isGPU {
		if gpuFrame.Resource == 0 {
			return nil
		}
		defer releaseCOM(unsafe.Pointer(gpuFrame.Resource))
		return v.renderer.RenderD3D11(gpuFrame, cursor)
	}
	if len(cpuFrame.BGRA) == 0 {
		return nil
	}
	return v.renderer.Render(cpuFrame)
}

func (v *windowsViewer) releaseLatestD3D11() {
	v.frameMu.Lock()
	resource := v.latestD3D11.Resource
	v.latestD3D11 = D3D11Frame{}
	v.latestIsGPU = false
	v.frameMu.Unlock()
	if resource != 0 {
		releaseCOM(unsafe.Pointer(resource))
	}
}

func (v *windowsViewer) setError(err error) {
	if err == nil {
		return
	}
	v.errMu.Lock()
	if v.err == nil {
		v.err = err
	}
	v.errMu.Unlock()
}

func (v *windowsViewer) Submit(frame Frame) error {
	if v == nil {
		return ErrUnavailable
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Width != v.config.Width || frame.Height != v.config.Height {
		return fmt.Errorf("%w: native viewer is %dx%d, frame is %dx%d", ErrUnavailable, v.config.Width, v.config.Height, frame.Width, frame.Height)
	}
	select {
	case <-v.done:
		v.errMu.Lock()
		err := v.err
		v.errMu.Unlock()
		if err != nil {
			return err
		}
		return ErrUnavailable
	default:
	}

	bytes := frame.Stride * frame.Height
	copyFrame := Frame{
		BGRA:   make([]byte, bytes),
		Width:  frame.Width,
		Height: frame.Height,
		Stride: frame.Stride,
	}
	copy(copyFrame.BGRA, frame.BGRA[:bytes])

	v.frameMu.Lock()
	oldResource := v.latestD3D11.Resource
	v.latest = copyFrame
	v.latestD3D11 = D3D11Frame{}
	v.latestIsGPU = false
	v.frameMu.Unlock()
	if oldResource != 0 {
		releaseCOM(unsafe.Pointer(oldResource))
	}

	hwnd := win.HWND(v.hwnd.Load())
	if hwnd == 0 || win.PostMessage(hwnd, wmNativeViewerFrame, 0, 0) == 0 {
		return ErrUnavailable
	}
	return nil
}

func (v *windowsViewer) SubmitD3D11(frame D3D11Frame) error {
	if v == nil {
		return ErrUnavailable
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	if frame.Width != v.config.Width || frame.Height != v.config.Height {
		return fmt.Errorf("%w: native viewer is %dx%d, D3D11 frame is %dx%d", ErrUnavailable, v.config.Width, v.config.Height, frame.Width, frame.Height)
	}
	select {
	case <-v.done:
		return ErrUnavailable
	default:
	}

	resource := unsafe.Pointer(frame.Resource)
	retainCOM(resource)

	v.frameMu.Lock()
	oldResource := v.latestD3D11.Resource
	v.latest = Frame{}
	v.latestD3D11 = frame
	v.latestIsGPU = true
	v.frameMu.Unlock()
	if oldResource != 0 {
		releaseCOM(unsafe.Pointer(oldResource))
	}

	hwnd := win.HWND(v.hwnd.Load())
	if hwnd == 0 || win.PostMessage(hwnd, wmNativeViewerFrame, 0, 0) == 0 {
		return ErrUnavailable
	}
	return nil
}

func (v *windowsViewer) D3D11Device() uintptr {
	if v == nil || v.renderer == nil {
		return 0
	}
	return v.renderer.DeviceHandle()
}

func (v *windowsViewer) SupportsGPUCursor() bool {
	return v != nil && v.renderer != nil && v.renderer.SupportsGPUCursor()
}

func (v *windowsViewer) Viewport() Viewport {
	if v == nil {
		return Viewport{}
	}
	viewport := unpackViewport(v.viewport.Load())
	if viewport.Valid() {
		return viewport
	}
	width, height := v.config.ViewportWidth, v.config.ViewportHeight
	if width <= 0 {
		width = v.config.Width
	}
	if height <= 0 {
		height = v.config.Height
	}
	return Viewport{Width: width, Height: height}
}

func (v *windowsViewer) SetCursor(cursor CursorOverlay) error {
	if v == nil || !v.SupportsGPUCursor() {
		return ErrUnavailable
	}
	copyCursor := cursor
	copyCursor.Bitmap.Pix = append([]byte(nil), cursor.Bitmap.Pix...)

	v.frameMu.Lock()
	v.cursor = copyCursor
	renderGPU := v.latestIsGPU && v.latestD3D11.Resource != 0
	v.frameMu.Unlock()
	if !renderGPU {
		return nil
	}

	hwnd := win.HWND(v.hwnd.Load())
	if hwnd == 0 || win.PostMessage(hwnd, wmNativeViewerFrame, 0, 0) == 0 {
		return ErrUnavailable
	}
	return nil
}

func (v *windowsViewer) Focus() {
	if v == nil {
		return
	}
	hwnd := win.HWND(v.hwnd.Load())
	if hwnd != 0 {
		win.ShowWindow(hwnd, win.SW_SHOW)
		win.SetForegroundWindow(hwnd)
	}
}

func (v *windowsViewer) Done() <-chan struct{} {
	if v == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return v.done
}

func (v *windowsViewer) Close() error {
	if v == nil {
		return nil
	}
	v.closeOnce.Do(func() {
		hwnd := win.HWND(v.hwnd.Load())
		if hwnd != 0 {
			win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
		}
	})
	<-v.done
	v.errMu.Lock()
	defer v.errMu.Unlock()
	return v.err
}
