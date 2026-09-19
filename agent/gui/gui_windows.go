//go:build windows

package gui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
	"relayproxy/internal/webui"
)

const (
	windowClass = "webview" // class name registered by go-webview2
	trayUID     = 1

	// WM_TRAY_CALLBACK carries mouse events and menu commands for the tray icon.
	wmTrayCallback = win.WM_USER + 1024

	iconBig   = 1 // ICON_BIG
	iconSmall = 0 // ICON_SMALL
)

var (
	activeApp   atomic.Pointer[appWindow]
	prevWndProc uintptr
	showMsgID   uint32

	moddwmapi                 = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = moddwmapi.NewProc("DwmSetWindowAttribute")
	moduser32                 = windows.NewLazySystemDLL("user32.dll")
	procGetWindowText         = moduser32.NewProc("GetWindowTextW")
	procGetWindowTextLength   = moduser32.NewProc("GetWindowTextLengthW")
)

func setWindowDarkTitleBar(hwnd win.HWND, dark bool) {
	if hwnd == 0 {
		return
	}
	var val int32
	if dark {
		val = 1
	}
	// DWMWA_USE_IMMERSIVE_DARK_MODE = 20 (Windows 10 2004+ and Windows 11)
	// DWMWA_USE_IMMERSIVE_DARK_MODE_BEFORE_20H1 = 19 (Windows 10 1809-1909)
	_, _, _ = procDwmSetWindowAttribute.Call(uintptr(hwnd), 20, uintptr(unsafe.Pointer(&val)), 4)
	_, _, _ = procDwmSetWindowAttribute.Call(uintptr(hwnd), 19, uintptr(unsafe.Pointer(&val)), 4)
}


func systemPrefersDark() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	useLight, _, err := key.GetIntegerValue("AppsUseLightTheme")
	return err == nil && useLight == 0
}

func (a *appWindow) themeDark() bool {
	switch a.opts.theme() {
	case "light":
		return false
	case "system":
		return systemPrefersDark()
	default:
		return true
	}
}

func (a *appWindow) applyThemeMode(mode string) {
	a.opts.Theme = mode
	dark := a.themeDark()
	setWindowDarkTitleBar(a.hwnd, dark)
	a.eval("applyTheme(" + jsonString(a.opts.theme()) + ", false)")
}

type appWindow struct {
	bridge *bridge.UIBridge
	opts   Options

	w    webview2.WebView
	hwnd win.HWND

	forceExit    atomic.Bool
	trayTipShown bool

	mu           sync.RWMutex
	last         app.AgentStatus
	proxyUp      bool
	minimizeTray bool

	stopCh    chan struct{}
	stopOnce  sync.Once
	monitorMu sync.Mutex
	monitor   *connectionWindow
}

// Run opens the desktop window and blocks until it is closed. It must be called
// on the main goroutine: go-webview2 locks the main OS thread during package
// init, and WebView2 requires the window and its message loop to share it.
func Run(b *bridge.UIBridge, opts Options) error {
	if opts.Title == "" {
		opts.Title = "RelayProxy 代理客户端"
	}
	if opts.Width <= 0 {
		opts.Width = 1040
	}
	if opts.Height <= 0 {
		opts.Height = 720
	}

	a := &appWindow{
		bridge:       b,
		opts:         opts,
		minimizeTray: opts.MinimizeToTray,
		stopCh:       make(chan struct{}),
	}
	activeApp.Store(a)
	defer activeApp.Store(nil)

	if err := checkWebView2Runtime(); err != nil {
		return err
	}

	// WebView2 needs a writable profile directory. Without an explicit one it
	// tries the process working directory, which is C:\Windows\system32 when the
	// agent is started from the login-autostart Run key — and that fails with
	// E_ACCESSDENIED, leaving a blank window.
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "RelayProxy", "webview2")
	if os.Getenv("LOCALAPPDATA") == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, ".relayproxy", "webview2")
		}
	}
	_ = os.MkdirAll(dataDir, 0755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  dataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  opts.Title,
			Width:  uint(opts.Width),
			Height: uint(opts.Height),
			Center: true,
			// IconId must be non-zero: go-webview2's zero branch calls
			// LoadImageW with the arguments shifted by one, so it loads garbage.
			// 2 is the group goversioninfo actually emits into our agent image
			// (verified with a PE resource dump); applyWindowIcons() replaces it
			// with the transparent ICO from the embedded assets a moment later,
			// so the value here only has to be harmless, not pixel-perfect.
			IconId: 2,
		},
	})
	if w == nil {
		return errors.New("创建 WebView2 窗口失败，请确认已安装 Microsoft Edge WebView2 运行时")
	}
	a.w = w
	a.hwnd = win.HWND(w.Window())
	w.SetSize(opts.Width, opts.Height, webview2.HintMin)

	// Apply the transparent brand icon to the title bar and the taskbar preview.
	// A black-boxed icon here is exactly what the previous build shipped.
	a.applyWindowIcons()
	setWindowDarkTitleBar(a.hwnd, a.themeDark())

	// Subclass the window procedure so the close button can hide to the tray and
	// tray messages can be routed back into the app.
	prevWndProc = win.SetWindowLongPtr(a.hwnd, win.GWLP_WNDPROC, syscall.NewCallback(wndProc))

	showMsgID = win.RegisterWindowMessage(windows.StringToUTF16Ptr("RelayProxyAgentShowWindow"))

	a.addTrayIcon()
	a.registerBindings()
	a.pushStatusLoop()

	// Load the embedded page with Tailwind and the logo inlined, so the window is
	// fully offline and standalone.
	html, err := a.renderHTML()
	if err != nil {
		a.removeTrayIcon()
		w.Destroy()
		return err
	}
	w.SetHtml(html)

	// The page loads asynchronously; forward everything logged so far once it is
	// up, otherwise the log view would look empty on a fresh start.
	attachLogTap(b, a)

	if opts.StartMinimized {
		win.ShowWindow(a.hwnd, win.SW_HIDE)
	}

	log.Printf("[GUI] 桌面窗口已启动 (config=%s, minimized=%v)", b.ConfigPath(), opts.StartMinimized)

	w.Run()

	// Teardown after the message loop ends.
	detachLogTap(b)
	a.stopStatusLoop()
	a.closeConnections()
	a.removeTrayIcon()
	w.Destroy()
	log.Println("[GUI] 桌面窗口已关闭，正在停止代理...")
	if err := b.Close(); err != nil {
		log.Printf("[GUI] 停止代理时出错: %v", err)
	}
	return nil
}

// checkWebView2Runtime prevents go-webview2 from terminating the GUI process
// through log.Fatal when the architecture-specific Evergreen runtime is absent
// or cannot be loaded. This is especially important for Windows ARM64, where
// an x64 runtime/loader cannot be used by an ARM64 process.
func checkWebView2Runtime() error {
	version, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return fmt.Errorf("无法加载 Windows %s WebView2 Loader: %w", runtime.GOARCH, err)
	}
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("未检测到 Windows %s WebView2 Runtime，请安装对应架构的 Microsoft Edge WebView2 Runtime", runtime.GOARCH)
	}
	return nil
}

// ShowStartupError makes GUI-subsystem failures visible when the executable is
// launched from Explorer, where stdout/stderr are not attached to a console.
func ShowStartupError(err error, fallbackURL string) {
	if err == nil {
		return
	}
	message := "RelayProxy 桌面界面无法启动。\n\n原因：" + err.Error()
	if fallbackURL != "" {
		message += "\n\nAgent 仍会继续运行，请在浏览器打开：\n" + fallbackURL
	}
	text, textErr := windows.UTF16PtrFromString(message)
	title, titleErr := windows.UTF16PtrFromString("RelayProxy")
	if textErr == nil && titleErr == nil {
		win.MessageBox(0, text, title, win.MB_OK|win.MB_ICONERROR)
	}
}

// RequestQuit terminates the active native UI event loop. It is used by the
// browser management page so its exit action has the same process-level effect
// as choosing Exit from the tray menu.
func RequestQuit() {
	if a := activeApp.Load(); a != nil {
		a.quit()
	}
}

// RequestRestart launches a replacement process before terminating the native
// window. The replacement waits for this process to exit, which releases the
// singleton mutex and all local listeners before it starts.
func RequestRestart() error {
	a := activeApp.Load()
	if a == nil {
		return errors.New("RelayProxy 桌面窗口尚未启动")
	}
	if err := launchRestartProcess(); err != nil {
		return err
	}
	log.Println("[GUI] 用户请求重启客户端")
	a.forceExit.Store(true)
	a.quit()
	return nil
}

// ---------------------------------------------------------------------------
// HTML assembly
// ---------------------------------------------------------------------------

func (a *appWindow) renderHTML() (string, error) {
	raw, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return "", fmt.Errorf("读取内嵌界面失败: %w", err)
	}
	html := string(raw)

	// Native WebView2 has no HTTP origin for /ui assets. Inline the exact same
	// shared design system used by the browser-hosted Agent and Server console.
	if sharedCSS, err := webui.ReadAsset("base.css"); err == nil {
		html = strings.Replace(html, "</head>", "<style>"+string(sharedCSS)+"</style></head>", 1)
	} else {
		return "", err
	}
	if sharedTheme, err := webui.ReadAsset("theme.js"); err == nil {
		html = strings.Replace(html, "</head>", "<script>"+string(sharedTheme)+"</script></head>", 1)
	} else {
		return "", err
	}

	if script, err := assets.ReadFile("assets/routing.js"); err == nil {
		html = strings.Replace(html, `<script src="routing.js"></script>`, "<script>"+string(script)+"</script>", 1)
	} else {
		return "", err
	}

	// Inline Tailwind: no CDN, no network, no sibling files.
	if idx := strings.Index(html, `<script src="tailwind.js"></script>`); idx >= 0 {
		html = strings.Replace(html, `<script src="tailwind.js"></script>`, "<script>"+string(tailwindJS)+"</script>", 1)
	}
	// Inline the logo so the brand mark renders without a file server.
	if logo, err := assets.ReadFile("assets/icon.png"); err == nil && len(logo) > 0 {
		dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(logo)
		html = strings.ReplaceAll(html, `src="icon.png"`, `src="`+dataURI+`"`)
	}
	// Honour the configured theme before first paint. The shared theme script
	// reads data-theme-mode, and "system" resolves through the native OS setting.
	mode := a.opts.theme()
	dark := a.themeDark()
	classAttr := ""
	if dark {
		classAttr = ` class="dark"`
	}
	html = strings.Replace(html, `<html lang="zh-CN" class="dark">`,
		`<html lang="zh-CN"`+classAttr+` data-theme-mode="`+mode+`">`, 1)
	return html, nil
}

// ---------------------------------------------------------------------------
// ui interface
// ---------------------------------------------------------------------------

func (a *appWindow) eval(script string) {
	if a.w == nil || script == "" {
		return
	}
	a.w.Dispatch(func() {
		defer func() { _ = recover() }()
		if a.w != nil {
			a.w.Eval(script)
		}
	})
}

func (a *appWindow) pushLog(line string) {
	a.eval("window.onGoLog && window.onGoLog(" + jsonString(line) + ")")
}

func (a *appWindow) applyTheme(dark bool) {
	setWindowDarkTitleBar(a.hwnd, dark)
	a.eval("applyTheme(" + fmt.Sprintf("%v", dark) + ", false)")
}

func (a *appWindow) refreshSidebar() {
	a.eval("window.onGoStatus && window.onGoStatus(" + a.statusJSON() + ")")
}

func (a *appWindow) setAutostartChecked(enabled bool) {
	a.eval("syncAutostart(" + fmt.Sprintf("%v", enabled) + ")")
}

func (a *appWindow) showWindow(visible bool) {
	if visible {
		win.ShowWindow(a.hwnd, win.SW_RESTORE)
		win.SetForegroundWindow(a.hwnd)
	} else {
		win.ShowWindow(a.hwnd, win.SW_HIDE)
	}
}

// ---------------------------------------------------------------------------
// Status push
// ---------------------------------------------------------------------------

func (a *appWindow) pushStatusLoop() {
	go func() {
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-a.stopCh:
				return
			case <-ticker.C:
				a.eval("window.onGoStatus && window.onGoStatus(" + a.statusJSON() + ")")
			}
		}
	}()
}

func (a *appWindow) stopStatusLoop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
}

func (a *appWindow) statusJSON() string {
	st := a.bridge.GetStatus()
	a.mu.Lock()
	a.last = st
	a.proxyUp = st.SOCKS5Running || st.HTTPRunning
	a.mu.Unlock()
	data, err := json.Marshal(st)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func (a *appWindow) snapshot() trayState {
	// Sample the mutable fields under the lock, then do the Win32 and registry
	// reads outside it so the message thread is never blocked by a lock held by
	// the 1.5s status ticker.
	a.mu.RLock()
	last := a.last
	proxyUp := a.proxyUp
	minimizeTray := a.minimizeTray
	a.mu.RUnlock()

	return trayState{
		DeviceID:       last.DeviceID,
		Mode:           last.Mode,
		TunnelUp:       last.Connected,
		ProxyUp:        proxyUp,
		WindowVisible:  win.IsWindowVisible(a.hwnd),
		AutoStart:      a.bridge.IsAutoStart(),
		ThemeDark:      a.themeDark(),
		MinimizeToTray: minimizeTray,
	}
}

// ---------------------------------------------------------------------------
// Window icon
// ---------------------------------------------------------------------------

func (a *appWindow) applyWindowIcons() {
	cx := int(win.GetSystemMetrics(win.SM_CXICON))
	cy := int(win.GetSystemMetrics(win.SM_CYICON))
	if h := loadAppIcon(cx, cy); h != 0 {
		win.SendMessage(a.hwnd, win.WM_SETICON, iconBig, uintptr(h))
	}
	sx := int(win.GetSystemMetrics(win.SM_CXSMICON))
	sy := int(win.GetSystemMetrics(win.SM_CYSMICON))
	if h := loadAppIcon(sx, sy); h != 0 {
		win.SendMessage(a.hwnd, win.WM_SETICON, iconSmall, uintptr(h))
	}
}

// loadAppIcon returns a transparent HICON.
//
// It prefers the ICO embedded in this package. The temp file is rewritten on
// every launch on purpose: Windows caches icons by path, and a stale copy left
// behind by an older build would keep the previous logo on screen forever.
func loadAppIcon(cx, cy int) win.HICON {
	if ico, err := assets.ReadFile("assets/icon.ico"); err == nil && len(ico) > 0 {
		tmp := filepath.Join(os.TempDir(), "relayproxy_app_icon.ico")
		if err := os.WriteFile(tmp, ico, 0644); err == nil {
			ptr := windows.StringToUTF16Ptr(tmp)
			if cx > 0 && cy > 0 {
				if h := win.LoadImage(0, ptr, win.IMAGE_ICON, int32(cx), int32(cy), win.LR_LOADFROMFILE); h != 0 {
					return win.HICON(h)
				}
			}
			if h := win.LoadImage(0, ptr, win.IMAGE_ICON, 0, 0, win.LR_LOADFROMFILE|win.LR_DEFAULTSIZE); h != 0 {
				return win.HICON(h)
			}
		}
	}

	// Fall back to the icon resource embedded by goversioninfo.
	//
	// The group ID is not stable: goversioninfo numbers the groups it emits and
	// our agent image ends up on 2 while the server image lands on 1. Probing a
	// small fixed set is therefore cheaper and safer than trusting one ID, and
	// the name spellings cover resource compilers that register the group by
	// name instead of by ordinal. applyWindowIcons() overrides whatever the
	// window class picked up a moment later, so a miss here is not fatal.
	module := win.GetModuleHandle(nil)
	for _, id := range []uintptr{1, 2, 3} {
		if h := loadIconResource(module, win.MAKEINTRESOURCE(id), cx, cy); h != 0 {
			return h
		}
	}
	for _, name := range []string{"IDI_ICON1", "APPICON", "MAINICON"} {
		if h := loadIconResource(module, windows.StringToUTF16Ptr(name), cx, cy); h != 0 {
			return h
		}
	}
	// goversioninfo also mirrors the app icon onto IDI_APPLICATION, which is why
	// both images carry a 32512 group next to the numbered one.
	if h := loadIconResource(module, win.MAKEINTRESOURCE(win.IDI_APPLICATION), cx, cy); h != 0 {
		return h
	}
	return win.LoadIcon(0, win.MAKEINTRESOURCE(win.IDI_APPLICATION))
}

// loadIconResource loads an embedded RT_GROUP_ICON by ID or by name, preferring
// the exact requested size and falling back to the default size of the group.
func loadIconResource(module win.HINSTANCE, name *uint16, cx, cy int) win.HICON {
	if cx > 0 && cy > 0 {
		if h := win.LoadImage(module, name, win.IMAGE_ICON, int32(cx), int32(cy), win.LR_SHARED); h != 0 {
			return win.HICON(h)
		}
	}
	if h := win.LoadImage(module, name, win.IMAGE_ICON, 0, 0, win.LR_SHARED|win.LR_DEFAULTSIZE); h != 0 {
		return win.HICON(h)
	}
	return win.LoadIcon(module, name)
}

// ---------------------------------------------------------------------------
// Tray icon
// ---------------------------------------------------------------------------

func (a *appWindow) addTrayIcon() {
	size := int(win.GetSystemMetrics(win.SM_CXSMICON))
	if size < 16 {
		size = 16
	}
	nid := win.NOTIFYICONDATA{
		CbSize:           uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:             a.hwnd,
		UID:              trayUID,
		UFlags:           win.NIF_MESSAGE | win.NIF_ICON | win.NIF_TIP,
		UCallbackMessage: wmTrayCallback,
		HIcon:            loadAppIcon(size, size),
	}
	copy(nid.SzTip[:], windows.StringToUTF16("RelayProxy 代理客户端"))
	win.Shell_NotifyIcon(win.NIM_ADD, &nid)
}

func (a *appWindow) removeTrayIcon() {
	nid := win.NOTIFYICONDATA{
		CbSize: uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:   a.hwnd,
		UID:    trayUID,
	}
	win.Shell_NotifyIcon(win.NIM_DELETE, &nid)
}

func (a *appWindow) showTrayBalloon(title, info string) {
	nid := win.NOTIFYICONDATA{
		CbSize:      uint32(unsafe.Sizeof(win.NOTIFYICONDATA{})),
		HWnd:        a.hwnd,
		UID:         trayUID,
		UFlags:      win.NIF_INFO,
		DwInfoFlags: 1, // NIIF_INFO
	}
	copy(nid.SzInfoTitle[:], windows.StringToUTF16(title))
	copy(nid.SzInfo[:], windows.StringToUTF16(info))
	win.Shell_NotifyIcon(win.NIM_MODIFY, &nid)
}

func (a *appWindow) showTrayMenu() {
	var pt win.POINT
	win.GetCursorPos(&pt)

	hMenu := win.CreatePopupMenu()
	defer win.DestroyMenu(hMenu)
	populateTrayMenu(hMenu, buildTrayMenuModel(a.snapshot()))

	win.SetForegroundWindow(a.hwnd)
	cmd := win.TrackPopupMenu(
		hMenu,
		win.TPM_RETURNCMD|win.TPM_NONOTIFY|win.TPM_RIGHTBUTTON|win.TPM_BOTTOMALIGN|win.TPM_RIGHTALIGN,
		pt.X, pt.Y, 0, a.hwnd, nil,
	)
	win.PostMessage(a.hwnd, win.WM_NULL, 0, 0)
	if cmd != 0 {
		a.handleTrayCommand(uint32(cmd))
	}
}

func (a *appWindow) handleTrayCommand(cmd uint32) {
	switch cmd {
	case MenuShow:
		a.showWindow(!win.IsWindowVisible(a.hwnd))
	case MenuCopyID:
		id := strings.TrimSpace(a.bridge.GetStatus().DeviceID)
		if id == "" {
			a.showTrayBalloon("复制失败", "设备尚未配对")
			return
		}
		copyToClipboard(id)
		a.showTrayBalloon("已复制设备 ID", id)
	case MenuConfigDir:
		openDirectory(filepath.Dir(a.bridge.ConfigPath()))
	case MenuAutoStart:
		next := !a.bridge.IsAutoStart()
		if err := a.bridge.SetAutoStart(next); err != nil {
			a.showTrayBalloon("开机自启", "设置失败："+err.Error())
			return
		}
		label := "已关闭"
		if next {
			label = "已开启"
		}
		a.setAutostartChecked(next)
		a.showTrayBalloon("开机自启", label)
	case MenuMinimize:
		a.mu.Lock()
		a.minimizeTray = !a.minimizeTray
		now := a.minimizeTray
		a.mu.Unlock()
		if err := a.persistGUI(map[string]any{"minimizeToTray": now}); err != nil {
			log.Printf("[GUI] 保存托盘行为失败: %v", err)
		}
	case MenuTheme:
		dark := a.opts.theme() != "dark"
		a.opts.Theme = map[bool]string{true: "dark", false: "light"}[dark]
		a.applyTheme(dark)
		if err := a.persistGUI(map[string]any{"theme": a.opts.Theme}); err != nil {
			log.Printf("[GUI] 保存主题失败: %v", err)
		}
	case MenuExit:
		a.quit()
	}
}

// quit stops the message loop; teardown happens after Run returns.
func (a *appWindow) quit() {
	a.forceExit.Store(true)
	if a.w != nil {
		a.w.Terminate()
	}
}

// persistGUI patches the GUI section of the config file.
func (a *appWindow) persistGUI(patch map[string]any) error {
	data, err := json.Marshal(map[string]any{"gui": patch})
	if err != nil {
		return err
	}
	var in bridge.ConfigUpdate
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	// Only the theme/minimize fields are populated, so nothing else is touched.
	_, err = a.bridge.SaveConfig(in)
	return err
}

// ---------------------------------------------------------------------------
// Window procedure
// ---------------------------------------------------------------------------

func wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	a := activeApp.Load()
	if a == nil {
		return win.CallWindowProc(prevWndProc, win.HWND(hwnd), uint32(msg), wp, lp)
	}

	switch {
	case showMsgID != 0 && uint32(msg) == showMsgID:
		// A second launch asked us to surface the window.
		a.showWindow(true)
		return 0

	case msg == win.WM_CLOSE:
		if a.minimizeTray && !a.forceExit.Load() {
			win.ShowWindow(win.HWND(hwnd), win.SW_HIDE)
			if !a.trayTipShown {
				a.trayTipShown = true
				a.showTrayBalloon("RelayProxy 仍在后台运行", "已最小化到系统托盘。双击托盘图标可重新打开主界面，右键可退出。")
			}
			return 0
		}
		return win.CallWindowProc(prevWndProc, win.HWND(hwnd), uint32(msg), wp, lp)

	case msg == wmTrayCallback:
		switch lp {
		case win.WM_LBUTTONDOWN, win.WM_LBUTTONDBLCLK:
			a.showWindow(!win.IsWindowVisible(win.HWND(hwnd)))
		case win.WM_RBUTTONUP:
			a.showTrayMenu()
		}
		return 0
	}

	return win.CallWindowProc(prevWndProc, win.HWND(hwnd), uint32(msg), wp, lp)
}

// ---------------------------------------------------------------------------
// Window activation (single-instance policy lives in the caller)
// ---------------------------------------------------------------------------

// ActivateExistingWindow raises the window of an already-running client, even
// when it is currently hidden in the tray. Used when a second launch is refused
// by the single-instance mutex.
func ActivateExistingWindow() {
	// The first instance may still be creating WebView2 when the second one is
	// launched (especially during login). Give it a short window to publish its
	// native HWND instead of making the second double-click appear to do nothing.
	var hwnd win.HWND
	for attempt := 0; attempt < 40 && hwnd == 0; attempt++ {
		hwnd = findMainWindow()
		if hwnd == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if hwnd == 0 {
		return
	}
	// Ask the owning instance to surface itself: that covers the case where the
	// window is hidden in the tray, which a plain ShowWindow would miss.
	if msg := win.RegisterWindowMessage(windows.StringToUTF16Ptr("RelayProxyAgentShowWindow")); msg != 0 {
		if win.PostMessage(hwnd, msg, 0, 0) != 0 {
			return
		}
	}
	win.ShowWindow(hwnd, win.SW_RESTORE)
	win.SetForegroundWindow(hwnd)
}

// findMainWindow accepts both the stable title used by current builds and the
// version-suffixed title used by older builds. The class filter keeps an open
// connections monitor (which also uses WebView2) from receiving the wake-up.
func findMainWindow() win.HWND {
	class, err := windows.UTF16PtrFromString(windowClass)
	if err != nil {
		return 0
	}
	title, err := windows.UTF16PtrFromString(DefaultWindowTitle)
	if err != nil {
		return 0
	}
	if hwnd := win.FindWindow(class, title); hwnd != 0 {
		return hwnd
	}

	for hwnd := win.GetWindow(win.GetDesktopWindow(), win.GW_CHILD); hwnd != 0; hwnd = win.GetWindow(hwnd, win.GW_HWNDNEXT) {
		var className [64]uint16
		length, classErr := win.GetClassName(hwnd, &className[0], len(className))
		if classErr != nil || windows.UTF16ToString(className[:length]) != windowClass {
			continue
		}
		if strings.HasPrefix(getWindowText(hwnd), DefaultWindowTitle) {
			return hwnd
		}
	}
	return 0
}

func getWindowText(hwnd win.HWND) string {
	length, _, _ := procGetWindowTextLength.Call(uintptr(hwnd))
	if length == 0 {
		return ""
	}
	buffer := make([]uint16, int(length)+1)
	written, _, _ := procGetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	return windows.UTF16ToString(buffer[:written])
}

// ---------------------------------------------------------------------------
// Small shell helpers
// ---------------------------------------------------------------------------

func openDirectory(dir string) {
	if dir == "" {
		return
	}
	_ = exec.Command("explorer", dir).Start()
}

// copyToClipboard mirrors a string into the Windows clipboard. Clipboard access
// must stay on one OS thread, hence the LockOSThread.
func copyToClipboard(text string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	utf16, err := windows.UTF16FromString(text)
	if err != nil || len(utf16) == 0 {
		return
	}

	var hwnd win.HWND
	if app := activeApp.Load(); app != nil {
		hwnd = app.hwnd
	}

	opened := false
	for i := 0; i < 10; i++ {
		if win.OpenClipboard(hwnd) {
			opened = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !opened {
		return
	}
	defer win.CloseClipboard()

	if !win.EmptyClipboard() {
		return
	}
	size := uintptr(len(utf16)) * unsafe.Sizeof(utf16[0])
	hMem := win.GlobalAlloc(win.GMEM_MOVEABLE, size)
	if hMem == 0 {
		return
	}
	ptr := win.GlobalLock(hMem)
	if ptr == nil {
		win.GlobalFree(hMem)
		return
	}
	copy(unsafe.Slice((*uint16)(ptr), len(utf16)), utf16)
	win.GlobalUnlock(hMem)

	if win.SetClipboardData(win.CF_UNICODETEXT, win.HANDLE(hMem)) == 0 {
		win.GlobalFree(hMem)
	}
}

func populateTrayMenu(hMenu win.HMENU, model trayMenuModel) {
	info := win.MENUINFO{
		CbSize:  uint32(unsafe.Sizeof(win.MENUINFO{})),
		FMask:   win.MIM_STYLE,
		DwStyle: win.MNS_CHECKORBMP,
	}
	win.SetMenuInfo(hMenu, &info)
	for _, it := range model.Items {
		appendTrayMenuItem(hMenu, it)
	}
}

func appendTrayMenuItem(hMenu win.HMENU, it trayMenuItem) {
	pos := uint32(win.GetMenuItemCount(hMenu))
	item := win.MENUITEMINFO{CbSize: uint32(unsafe.Sizeof(win.MENUITEMINFO{}))}

	if it.Sep {
		item.FMask = win.MIIM_FTYPE
		item.FType = win.MFT_SEPARATOR
		win.InsertMenuItem(hMenu, pos, true, &item)
		return
	}

	ptr, err := windows.UTF16PtrFromString(it.Text)
	if err != nil {
		return
	}
	item.FMask = win.MIIM_STRING | win.MIIM_ID | win.MIIM_STATE | win.MIIM_FTYPE
	item.FType = win.MFT_STRING
	item.WID = it.ID
	item.DwTypeData = ptr
	if it.Disabled {
		item.FState |= win.MFS_DISABLED
	}
	if it.Checked {
		item.FState |= win.MFS_CHECKED
	}
	if it.Default {
		item.FState |= win.MFS_DEFAULT
	}
	win.InsertMenuItem(hMenu, pos, true, &item)
}
