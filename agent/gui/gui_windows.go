//go:build windows

package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing/fstest"
	"time"

	"github.com/lxn/win"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	agentapp "relayproxy/agent/app"
	"relayproxy/agent/bridge"
	"relayproxy/internal/webui"
)

const windowClass = "WailsWebviewWindow"

var (
	activeApp atomic.Pointer[appWindow]
	showMsgID uint32
)

type appWindow struct {
	bridge *bridge.UIBridge
	opts   Options

	app    *application.App
	window *application.WebviewWindow
	tray   *application.SystemTray

	forceExit atomic.Bool

	mu           sync.RWMutex
	last         agentapp.AgentStatus
	proxyUp      bool
	minimizeTray bool

	stopCh   chan struct{}
	stopOnce sync.Once

	monitorMu sync.Mutex
	monitor   *application.WebviewWindow

	desktopViewerMu sync.Mutex
	desktopViewer   *nativeDesktopSession
	desktopViewers  map[string]*nativeDesktopSession
}

func systemPrefersDark() bool {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		"Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize",
		registry.QUERY_VALUE,
	)
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

// Run opens the Windows desktop UI using Wails v3. The Agent core, UIBridge and
// browser management page remain unchanged; Wails only owns the native shell,
// system tray and Go<->JavaScript transport.
func Run(b *bridge.UIBridge, opts Options) error {
	if opts.Title == "" {
		opts.Title = DefaultWindowTitle
	}
	if opts.Width <= 0 {
		opts.Width = DefaultWindowWidth
	}
	if opts.Height <= 0 {
		opts.Height = DefaultWindowHeight
	}

	mainHTML, connectionsHTML, files, err := buildWailsAssets(opts)
	if err != nil {
		return err
	}
	files["index.html"] = &fstest.MapFile{Data: []byte(mainHTML), Mode: 0o444}
	files["connections.html"] = &fstest.MapFile{Data: []byte(connectionsHTML), Mode: 0o444}

	a := &appWindow{
		bridge:         b,
		opts:           opts,
		minimizeTray:   opts.MinimizeToTray,
		stopCh:         make(chan struct{}),
		desktopViewers: make(map[string]*nativeDesktopSession),
	}

	showMsgID = win.RegisterWindowMessage(windows.StringToUTF16Ptr("RelayProxyAgentShowWindow"))

	icon, _ := assets.ReadFile("assets/icon.png")
	trayIcon, trayErr := assets.ReadFile("assets/icon.ico")
	if trayErr != nil || len(trayIcon) == 0 {
		trayIcon = icon
	}
	dataPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "RelayProxy", "wails-webview2")
	if os.Getenv("LOCALAPPDATA") == "" {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			dataPath = filepath.Join(home, ".relayproxy", "wails-webview2")
		}
	}
	_ = os.MkdirAll(dataPath, 0o755)

	service := &WailsService{owner: a}
	wailsApp := application.New(application.Options{
		Name:        "RelayProxy",
		Description: "RelayProxy Agent",
		Icon:        icon,
		Assets: application.AssetOptions{
			Handler:        application.BundledAssetFileServer(files),
			DisableLogging: true,
		},
		Services: []application.Service{
			application.NewService(service),
		},
		Windows: application.WindowsOptions{
			WebviewUserDataPath: dataPath,
			WndProcInterceptor: func(_ uintptr, msg uint32, _, _ uintptr) (uintptr, bool) {
				if showMsgID != 0 && msg == showMsgID {
					a.showWindow(true)
					return 0, true
				}
				return 0, false
			},
		},
	})
	a.app = wailsApp

	window := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "main",
		Title:                      opts.Title,
		Width:                      opts.Width,
		Height:                     opts.Height,
		MinWidth:                   MinimumWindowWidth,
		MinHeight:                  MinimumWindowHeight,
		URL:                        "/",
		Hidden:                     opts.StartMinimized,
		InitialPosition:            application.WindowCentered,
		DefaultContextMenuDisabled: true,
		DevToolsEnabled:            false,
	})
	a.window = window

	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if a.forceExit.Load() {
			return
		}
		a.mu.RLock()
		minimize := a.minimizeTray
		a.mu.RUnlock()
		if minimize {
			event.Cancel()
			window.Hide()
			return
		}
		a.forceExit.Store(true)
		a.closeConnections()
	})

	a.installTray(trayIcon)

	activeApp.Store(a)
	defer activeApp.Store(nil)

	attachLogTap(b, a)
	defer detachLogTap(b)
	a.pushStatusLoop()
	defer a.stopStatusLoop()
	defer a.stopNativeDesktopViewer()

	if !opts.StartMinimized {
		window.Center()
		window.Show().Focus()
	}

	log.Printf("[GUI] Wails 桌面窗口已启动 (config=%s, minimized=%v)", b.ConfigPath(), opts.StartMinimized)
	err = wailsApp.Run()
	a.closeConnections()

	log.Println("[GUI] Wails 桌面窗口已关闭，正在停止代理...")
	if closeErr := b.Close(); closeErr != nil {
		log.Printf("[GUI] 停止代理时出错: %v", closeErr)
	}
	return err
}

func buildWailsAssets(opts Options) (string, string, fstest.MapFS, error) {
	index, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return "", "", nil, fmt.Errorf("读取内嵌界面失败: %w", err)
	}
	connections, err := assets.ReadFile("assets/connections.html")
	if err != nil {
		return "", "", nil, fmt.Errorf("读取连接监控界面失败: %w", err)
	}
	tailwind, err := assets.ReadFile("assets/tailwind.js")
	if err != nil {
		return "", "", nil, err
	}
	routing, err := assets.ReadFile("assets/routing.js")
	if err != nil {
		return "", "", nil, err
	}
	connectionsJS, err := assets.ReadFile("assets/connections.js")
	if err != nil {
		return "", "", nil, err
	}
	bridgeJS, err := assets.ReadFile("assets/wails-bridge.js")
	if err != nil {
		return "", "", nil, err
	}
	baseCSS, err := webui.ReadAsset("base.css")
	if err != nil {
		return "", "", nil, err
	}
	themeJS, err := webui.ReadAsset("theme.js")
	if err != nil {
		return "", "", nil, err
	}

	inject := "<link rel=\"stylesheet\" href=\"/ui/base.css\">" +
		"<script src=\"/ui/theme.js\"></script>" +
		"<script src=\"/wails/runtime.js\" type=\"module\"></script>" +
		"<script src=\"/wails-bridge.js\" defer></script>"

	mainHTML := strings.Replace(string(index), "<head>", "<head>"+inject, 1)
	mode := opts.theme()
	dark := mode == "dark" || (mode == "system" && systemPrefersDark())
	classAttr := ""
	if dark {
		classAttr = " class=\"dark\""
	}
	mainHTML = strings.Replace(
		mainHTML,
		"<html lang=\"zh-CN\" class=\"dark\">",
		"<html lang=\"zh-CN\""+classAttr+" data-theme-mode=\""+mode+"\">",
		1,
	)

	connectionsHTML := strings.Replace(string(connections), "<head>", "<head>"+inject, 1)
	if !dark {
		connectionsHTML = strings.Replace(connectionsHTML, "data-theme=\"dark\"", "data-theme=\"light\"", 1)
	}

	files := fstest.MapFS{
		"tailwind.js":     &fstest.MapFile{Data: tailwind, Mode: 0o444},
		"routing.js":      &fstest.MapFile{Data: routing, Mode: 0o444},
		"connections.js":  &fstest.MapFile{Data: connectionsJS, Mode: 0o444},
		"wails-bridge.js": &fstest.MapFile{Data: bridgeJS, Mode: 0o444},
		"ui/base.css":     &fstest.MapFile{Data: baseCSS, Mode: 0o444},
		"ui/theme.js":     &fstest.MapFile{Data: themeJS, Mode: 0o444},
	}
	return mainHTML, connectionsHTML, files, nil
}

func (a *appWindow) installTray(icon []byte) {
	if a.app == nil {
		return
	}
	menu := a.app.NewMenu()

	menu.Add("打开主界面").OnClick(func(*application.Context) {
		a.showWindow(true)
	})
	menu.Add("复制设备 ID").OnClick(func(*application.Context) {
		id := strings.TrimSpace(a.bridge.GetStatus().DeviceID)
		if id != "" && a.app != nil {
			a.app.Clipboard.SetText(id)
		}
	})
	menu.Add("打开配置目录").OnClick(func(*application.Context) {
		openDirectory(filepath.Dir(a.bridge.ConfigPath()))
	})
	menu.AddSeparator()

	autostart := menu.AddCheckbox("开机自启", a.bridge.IsAutoStart())
	autostart.OnClick(func(*application.Context) {
		enabled := autostart.Checked()
		if err := a.bridge.SetAutoStart(enabled); err != nil {
			log.Printf("[GUI] 设置开机自启失败: %v", err)
			autostart.SetChecked(!enabled)
			return
		}
		a.setAutostartChecked(enabled)
	})

	minimize := menu.AddCheckbox("关闭时最小化到托盘", a.minimizeTray)
	minimize.OnClick(func(*application.Context) {
		enabled := minimize.Checked()
		a.mu.Lock()
		a.minimizeTray = enabled
		a.mu.Unlock()
		if err := a.persistGUI(map[string]any{"minimizeToTray": enabled}); err != nil {
			log.Printf("[GUI] 保存托盘行为失败: %v", err)
		}
	})

	menu.Add("切换浅色/深色").OnClick(func(*application.Context) {
		next := "light"
		if a.opts.theme() != "dark" {
			next = "dark"
		}
		a.applyThemeMode(next)
		if err := a.persistGUI(map[string]any{"theme": next}); err != nil {
			log.Printf("[GUI] 保存主题失败: %v", err)
		}
	})
	menu.AddSeparator()
	menu.Add("退出").OnClick(func(*application.Context) {
		a.quit()
	})

	tray := a.app.SystemTray.New()
	tray.SetIcon(icon)
	tray.SetTooltip("RelayProxy")
	tray.SetMenu(menu)
	tray.OnClick(func() {
		if a.window == nil {
			return
		}
		a.showWindow(!a.window.IsVisible())
	})
	a.tray = tray
}

func (a *appWindow) applyThemeMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "light", "dark", "system":
	default:
		mode = "system"
	}
	a.opts.Theme = mode
	if a.window != nil {
		a.window.ExecJS("window.applyTheme && window.applyTheme(" + jsonString(mode) + ", false)")
	}
}

func (a *appWindow) pushLog(line string) {
	a.eval("window.onGoLog && window.onGoLog(" + jsonString(line) + ")")
}

func (a *appWindow) applyTheme(dark bool) {
	mode := "light"
	if dark {
		mode = "dark"
	}
	a.applyThemeMode(mode)
}

func (a *appWindow) refreshSidebar() {
	a.eval("window.onGoStatus && window.onGoStatus(" + a.statusJSON() + ")")
}

func (a *appWindow) setAutostartChecked(enabled bool) {
	a.eval("window.syncAutostart && window.syncAutostart(" + fmt.Sprintf("%v", enabled) + ")")
}

func (a *appWindow) showWindow(visible bool) {
	if a.window == nil {
		return
	}
	if visible {
		a.window.UnMinimise()
		a.window.Show().Focus()
		return
	}
	a.window.Hide()
}

func (a *appWindow) eval(script string) {
	if a.window == nil || script == "" {
		return
	}
	a.window.ExecJS(script)
}

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

func (a *appWindow) persistGUI(patch map[string]any) error {
	data, err := json.Marshal(map[string]any{"gui": patch})
	if err != nil {
		return err
	}
	var in bridge.ConfigUpdate
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	_, err = a.bridge.SaveConfig(in)
	return err
}

func (a *appWindow) quit() {
	if a == nil || a.app == nil {
		return
	}
	a.forceExit.Store(true)
	a.closeConnections()
	a.app.Quit()
}

// RequestQuit terminates the active Wails application.
func RequestQuit() {
	if a := activeApp.Load(); a != nil {
		a.quit()
	}
}

// RequestRestart starts a replacement process then exits the Wails shell.
func RequestRestart() error {
	a := activeApp.Load()
	if a == nil {
		return errors.New("RelayProxy 桌面窗口尚未启动")
	}
	if err := launchRestartProcess(); err != nil {
		return err
	}
	log.Println("[GUI] 用户请求重启客户端")
	a.quit()
	return nil
}

// ActivateExistingWindow raises an already-running Wails window after singleton
// acquisition rejects a second Agent process.
func ActivateExistingWindow() {
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
	msg := win.RegisterWindowMessage(windows.StringToUTF16Ptr("RelayProxyAgentShowWindow"))
	if msg != 0 && win.PostMessage(hwnd, msg, 0, 0) != 0 {
		return
	}
	win.ShowWindow(hwnd, win.SW_RESTORE)
	win.SetForegroundWindow(hwnd)
}

func findMainWindow() win.HWND {
	class, err := windows.UTF16PtrFromString(windowClass)
	if err != nil {
		return 0
	}
	title, err := windows.UTF16PtrFromString(DefaultWindowTitle)
	if err != nil {
		return 0
	}
	return win.FindWindow(class, title)
}

// ShowStartupError makes GUI-subsystem failures visible when launched from
// Explorer where stdout/stderr are not attached to a console.
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

func openDirectory(dir string) {
	if dir == "" {
		return
	}
	_ = exec.Command("explorer", dir).Start()
}
