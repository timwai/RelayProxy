package gui

import "strings"

// Tray menu command identifiers.
const (
	MenuShow        = 1001
	MenuCopyID      = 1002
	MenuConfigDir   = 1003
	MenuAutoStart   = 1004
	MenuTheme       = 1005
	MenuMinimize    = 1006
	MenuExit        = 1007
)

// trayState is a plain snapshot of everything the tray menu renders, so the
// menu model can be built and tested without any Win32 calls.
type trayState struct {
	DeviceID       string
	Mode           string
	TunnelUp       bool
	ProxyUp        bool
	WindowVisible  bool
	AutoStart      bool
	ThemeDark      bool
	MinimizeToTray bool
}

type trayMenuItem struct {
	ID       uint32
	Text     string
	Sep      bool
	Disabled bool
	Checked  bool
	Default  bool
}

type trayMenuModel struct {
	Items []trayMenuItem
}

func buildTrayMenuModel(s trayState) trayMenuModel {
	device := strings.TrimSpace(s.DeviceID)
	if device == "" {
		device = "未配对"
	}
	mode := strings.TrimSpace(s.Mode)
	if mode == "" {
		mode = "-"
	}

	tunnel := "未连接"
	if s.TunnelUp {
		tunnel = "已连接"
	}
	proxy := "已停止"
	if s.ProxyUp {
		proxy = "运行中"
	}

	showText := "打开主界面"
	if s.WindowVisible {
		showText = "隐藏到托盘"
	}
	themeText := "浅色界面"
	if !s.ThemeDark {
		themeText = "深色界面"
	}

	return trayMenuModel{Items: []trayMenuItem{
		{Text: "RelayProxy  ·  " + device + "  [" + mode + "]", Disabled: true},
		{Text: "中继隧道：" + tunnel + "    本地代理：" + proxy, Disabled: true},
		{Sep: true},
		{ID: MenuShow, Text: showText, Default: !s.WindowVisible},
		{Sep: true},
		{ID: MenuCopyID, Text: "复制设备 ID", Disabled: strings.TrimSpace(s.DeviceID) == ""},
		{ID: MenuConfigDir, Text: "打开配置目录"},
		{Sep: true},
		{ID: MenuAutoStart, Text: "开机自启", Checked: s.AutoStart},
		{ID: MenuMinimize, Text: "关闭时最小化到托盘", Checked: s.MinimizeToTray},
		{ID: MenuTheme, Text: themeText},
		{Sep: true},
		{ID: MenuExit, Text: "退出"},
	}}
}
