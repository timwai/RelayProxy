//go:build windows

package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/jchv/go-webview2"
	"github.com/lxn/win"
)

type connectionWindow struct {
	owner    *appWindow
	dark     bool
	hwnd     atomic.Uintptr
	closing  atomic.Bool
	done     chan struct{}
	previous uintptr
}

var connectionWindows sync.Map
var connectionWndProcPointer = syscall.NewCallback(connectionWndProc)

func connectionWndProc(hwnd win.HWND, message uint32, wparam, lparam uintptr) uintptr {
	value, ok := connectionWindows.Load(hwnd)
	if !ok {
		return win.DefWindowProc(hwnd, message, wparam, lparam)
	}
	m := value.(*connectionWindow)
	if message == win.WM_CLOSE && !m.closing.Load() {
		// Reuse the WebView on reopen. The WebView2 wrapper retains native
		// contexts, so repeatedly allocating new views would accumulate them.
		win.ShowWindow(hwnd, win.SW_HIDE)
		return 0
	}
	result := win.CallWindowProc(m.previous, hwnd, message, wparam, lparam)
	if message == win.WM_NCDESTROY {
		connectionWindows.Delete(hwnd)
	}
	return result
}

func (a *appWindow) openConnections() {
	a.monitorMu.Lock()
	defer a.monitorMu.Unlock()
	select {
	case <-a.stopCh:
		return
	default:
	}
	if a.monitor != nil {
		if hwnd := win.HWND(a.monitor.hwnd.Load()); hwnd != 0 {
			win.ShowWindow(hwnd, win.SW_RESTORE)
			win.SetForegroundWindow(hwnd)
		}
		return
	}
	m := &connectionWindow{owner: a, dark: a.opts.theme() == "dark", done: make(chan struct{})}
	a.monitor = m
	go m.run()
}

func (m *connectionWindow) run() {
	// Each WebView owns an STA and a message loop. Sharing the main window's
	// thread would let a monitor WM_QUIT stop the main application's loop.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer func() {
		m.hwnd.Store(0)
		m.owner.monitorMu.Lock()
		if m.owner.monitor == m {
			m.owner.monitor = nil
		}
		m.owner.monitorMu.Unlock()
		close(m.done)
	}()
	fail := func(err error) {
		log.Printf("[GUI] 实时连接窗口: %v", err)
		m.owner.eval("window.showMonitorError && window.showMonitorError(" + jsonString(err.Error()) + ")")
	}
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		if code, ok := err.(*ole.OleError); !ok || code.Code() != 1 {
			fail(err)
			return
		}
	}
	defer ole.CoUninitialize()
	html, err := renderConnectionsHTML(m.dark)
	if err != nil {
		fail(err)
		return
	}
	dataDir, err := os.UserCacheDir()
	if err != nil {
		fail(err)
		return
	}
	dataDir = filepath.Join(dataDir, "RelayProxy", "webview2-connections")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fail(err)
		return
	}
	width := min(1380, max(900, int(win.GetSystemMetrics(win.SM_CXSCREEN))-80))
	height := min(820, max(500, int(win.GetSystemMetrics(win.SM_CYSCREEN))-100))
	w := webview2.NewWithOptions(webview2.WebViewOptions{DataPath: dataDir, AutoFocus: true,
		WindowOptions: webview2.WindowOptions{Title: "RelayProxy · 实时连接", Width: uint(width), Height: uint(height), Center: true, IconId: 2}})
	if w == nil {
		fail(fmt.Errorf("无法创建实时连接窗口，请检查 WebView2 运行时"))
		return
	}
	hwnd := win.HWND(w.Window())
	m.previous = win.SetWindowLongPtr(hwnd, win.GWLP_WNDPROC, connectionWndProcPointer)
	connectionWindows.Store(hwnd, m)
	w.SetSize(850, 420, webview2.HintMin)
	setWindowDarkTitleBar(hwnd, m.dark)
	if icon := loadAppIcon(32, 32); icon != 0 {
		win.SendMessage(hwnd, win.WM_SETICON, iconBig, uintptr(icon))
	}
	if err := w.Bind("goGetConnections", func() (string, error) {
		if !win.IsWindowVisible(hwnd) {
			return "", nil
		}
		snapshot := m.owner.bridge.GetConnections()
		data, err := json.Marshal(snapshot)
		return string(data), err
	}); err != nil {
		win.DestroyWindow(hwnd)
		fail(err)
		return
	}
	w.SetHtml(html)
	m.hwnd.Store(uintptr(hwnd))
	if m.closing.Load() {
		win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
	}
	w.Run()
	// WM_CLOSE normally destroyed it already; explicit destruction also covers
	// an external WM_QUIT and always happens on the window's owning STA.
	win.DestroyWindow(hwnd)
}

func (a *appWindow) closeConnections() {
	a.monitorMu.Lock()
	m := a.monitor
	if m != nil {
		m.closing.Store(true)
	}
	a.monitorMu.Unlock()
	if m == nil {
		return
	}
	if hwnd := win.HWND(m.hwnd.Load()); hwnd != 0 {
		win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
	}
	select {
	case <-m.done:
	case <-time.After(5 * time.Second):
		log.Print("[GUI] 正在结束实时连接窗口")
	}
}

func renderConnectionsHTML(dark bool) (string, error) {
	raw, err := assets.ReadFile("assets/connections.html")
	if err != nil {
		return "", err
	}
	script, err := assets.ReadFile("assets/connections.js")
	if err != nil {
		return "", err
	}
	html := strings.Replace(string(raw), `<script src="connections.js"></script>`, "<script>"+string(script)+"</script>", 1)
	if !dark {
		html = strings.Replace(html, `data-theme="dark"`, `data-theme="light"`, 1)
	}
	return html, nil
}
