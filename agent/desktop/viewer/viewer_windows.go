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
)

const (
	nativeViewerClass = "RelayProxyNativeViewerWindow"

	wmNativeViewerFrame = win.WM_APP + 0x311
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

	hwnd atomic.Uintptr

	frameMu sync.Mutex
	latest  Frame

	renderer *d3d11Renderer

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
		config: config,
		done:   make(chan struct{}),
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

	style := uint32(win.WS_OVERLAPPED | win.WS_CAPTION | win.WS_SYSMENU | win.WS_MINIMIZEBOX)
	rect := win.RECT{Left: 0, Top: 0, Right: int32(v.config.Width), Bottom: int32(v.config.Height)}
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
		renderer.Close()
		v.renderer = nil
		v.hwnd.Store(0)
	}()

	win.ShowWindow(hwnd, win.SW_SHOW)
	win.UpdateWindow(hwnd)
	win.SetForegroundWindow(hwnd)
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
	switch msg {
	case wmNativeViewerFrame:
		if value, ok := nativeViewerWindows.Load(uintptr(hwnd)); ok {
			viewer := value.(*windowsViewer)
			if err := viewer.renderLatest(); err != nil {
				viewer.setError(err)
				win.DestroyWindow(hwnd)
			}
		}
		return 0

	case win.WM_ERASEBKGND:
		return 1

	case win.WM_CLOSE:
		win.DestroyWindow(hwnd)
		return 0

	case win.WM_DESTROY:
		win.PostQuitMessage(0)
		return 0
	}
	return win.DefWindowProc(hwnd, msg, wParam, lParam)
}

func (v *windowsViewer) renderLatest() error {
	v.frameMu.Lock()
	frame := v.latest
	v.frameMu.Unlock()
	if len(frame.BGRA) == 0 {
		return nil
	}
	if v.renderer == nil {
		return ErrUnavailable
	}
	return v.renderer.Render(frame)
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
	v.latest = copyFrame
	v.frameMu.Unlock()

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
