//go:build windows

package gui

import (
	"encoding/json"
	"log"
	"path/filepath"
	"strings"

	"relayproxy/agent/bridge"
	"relayproxy/agent/divert"
)

const okResult = "ok"

// WailsService is the Windows frontend service registered with Wails v3.
// The HTML adapter keeps the legacy window.go* contract so the existing UI can
// migrate without coupling its application logic to a specific desktop shell.
type WailsService struct {
	owner *appWindow
}

func (s *WailsService) OpenConnections() string {
	if s == nil || s.owner == nil {
		return "GUI unavailable"
	}
	s.owner.openConnections()
	return okResult
}

func (s *WailsService) GetConnections() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "{}", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetConnections())
	if err != nil {
		return "{}", nil
	}
	return string(data), nil
}

func (s *WailsService) ClearConnections() {
	if s != nil && s.owner != nil && s.owner.bridge != nil {
		s.owner.bridge.ClearConnections()
	}
}

func (s *WailsService) GetStatus() (string, error) {
	if s == nil || s.owner == nil {
		return "{}", nil
	}
	return s.owner.statusJSON(), nil
}

func (s *WailsService) GetDiagnostics() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "{}", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetDiagnostics())
	if err != nil {
		return "{}", nil
	}
	return string(data), nil
}

func (s *WailsService) RunSpeedTest(exitID string, durationSeconds int) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	result, err := s.owner.bridge.RunSpeedTest(exitID, durationSeconds)
	if err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	data, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		return `{"ok":false,"message":"failed to encode speed test result"}`, nil
	}
	return string(data), nil
}

func (s *WailsService) GetProxyExits() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "[]", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetProxyExits())
	if err != nil {
		return "[]", nil
	}
	return string(data), nil
}

func (s *WailsService) GetRDPTargets() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "[]", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetRDPTargets())
	if err != nil {
		return "[]", nil
	}
	return string(data), nil
}

func (s *WailsService) ConnectRDP(targetID string, autoLaunch bool) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	target, err := s.owner.bridge.ConnectRDP(targetID, autoLaunch)
	return rdpActionResponse(map[string]any{"target": target}, err), nil
}

func (s *WailsService) DisconnectRDP() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	s.owner.bridge.DisconnectRDP()
	return rdpActionResponse(nil, nil), nil
}

func (s *WailsService) GetMessages() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "[]", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetMessages(500))
	if err != nil {
		return "[]", nil
	}
	return string(data), nil
}

func (s *WailsService) ClearMessages() {
	if s != nil && s.owner != nil && s.owner.bridge != nil {
		s.owner.bridge.ClearMessages()
	}
}

func (s *WailsService) HideVerificationPopup() {
	if s != nil && s.owner != nil {
		s.owner.hideVerificationPopup()
	}
}

func (s *WailsService) GetLogs() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "[]", nil
	}
	entries := s.owner.bridge.GetLogs(500)
	data, err := json.Marshal(entries)
	if err != nil {
		return "[]", nil
	}
	return string(data), nil
}

func (s *WailsService) ClearLogs() {
	if s != nil && s.owner != nil && s.owner.bridge != nil {
		s.owner.bridge.ClearLogs()
	}
}

func (s *WailsService) GetConfig() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "{}", nil
	}
	a := s.owner
	state, err := a.bridge.GetConfigState()
	if err != nil {
		data, _ := json.Marshal(map[string]any{
			"configError": err.Error(),
			"configPath":  a.bridge.ConfigPath(),
		})
		return string(data), nil
	}
	cfg := state.Config

	type proxyLeg struct {
		Enabled bool   `json:"enabled"`
		Listen  string `json:"listen"`
		Port    int    `json:"port"`
	}
	payload := struct {
		ConfigPath                  string              `json:"configPath"`
		ServerAddress               string              `json:"serverAddress"`
		QUICPort                    int                 `json:"quicPort"`
		TCPPort                     int                 `json:"tcpPort"`
		TLSEnabled                  bool                `json:"tlsEnabled"`
		InsecureTLS                 bool                `json:"insecureTls"`
		DeviceName                  string              `json:"deviceName"`
		IdentityID                  string              `json:"identityId"`
		Transport                   string              `json:"transport"`
		SOCKS5                      proxyLeg            `json:"socks5"`
		HTTP                        proxyLeg            `json:"http"`
		DefaultExitID               string              `json:"defaultExitId"`
		ExitEnabled                 bool                `json:"exitEnabled"`
		AllowInternet               bool                `json:"allowInternet"`
		AllowPrivate                bool                `json:"allowPrivateNetwork"`
		AllowLoopback               bool                `json:"allowLoopback"`
		AccessMode                  string              `json:"accessMode"`
		AccessDomains               []string            `json:"accessDomains"`
		AccessCIDRs                 []string            `json:"accessCidrs"`
		ExitUpstream                any                 `json:"exitUpstream"`
		P2P                         any                 `json:"p2p"`
		PublicDirectAdvertise       string              `json:"publicDirectAdvertise"`
		NetworkMode                 string              `json:"networkMode"`
		IsAutostart                 bool                `json:"isAutostart"`
		MinimizeToTray              bool                `json:"minimizeToTray"`
		StartMinimized              bool                `json:"startMinimized"`
		Theme                       string              `json:"theme"`
		VerificationPopupTimeoutSec int                 `json:"verificationPopupTimeoutSec"`
		Version                     string              `json:"version"`
		Routing                     any                 `json:"routing"`
		Network                     any                 `json:"network"`
		NetworkCapabilities         divert.Capabilities `json:"networkCapabilities"`
		Runtime                     any                 `json:"runtime"`
		Revision                    string              `json:"revision"`
		RestartRequired             bool                `json:"restartRequired"`
		RestartFields               []string            `json:"restartFields"`
		ReloadPending               bool                `json:"reloadPending"`
	}{
		ConfigPath:    a.bridge.ConfigPath(),
		ServerAddress: cfg.Server.Address,
		QUICPort:      cfg.Server.QUICPort,
		TCPPort:       cfg.Server.TCPPort,
		TLSEnabled:    cfg.IsServerTLSEnabled(),
		InsecureTLS:   cfg.Server.InsecureTLS,
		DeviceName:    cfg.Device.Name,
		IdentityID:    cfg.Device.IdentityID,
		Transport:     cfg.Transport.Mode,
		DefaultExitID: cfg.Proxy.DefaultExitID,
		ExitEnabled:   cfg.Exit.Enabled == nil || *cfg.Exit.Enabled,
		AllowInternet: cfg.Exit.AllowInternet,
		AllowPrivate:  cfg.Exit.AllowPrivateNetwork,
		AllowLoopback: cfg.Exit.AllowLoopback,
		AccessMode:    cfg.Exit.Access.Mode,
		AccessDomains: cfg.Exit.Access.Domains,
		AccessCIDRs:   cfg.Exit.Access.CIDRs,
		ExitUpstream: map[string]any{
			"mode": cfg.Exit.Upstream.Mode, "address": cfg.Exit.Upstream.Address,
			"username": cfg.Exit.Upstream.Username, "password": cfg.Exit.Upstream.Password,
		},
		P2P: map[string]any{
			"enabled": cfg.P2P.Enabled == nil || *cfg.P2P.Enabled, "mode": cfg.P2P.Mode,
			"punchTimeoutMs": cfg.P2P.PunchTimeoutMs, "keepaliveSec": cfg.P2P.KeepaliveSec,
			"idleTimeoutSec": cfg.P2P.IdleTimeoutSec, "maxExitSessions": cfg.P2P.MaxExitSessions,
			"fallback": cfg.P2P.Fallback == nil || *cfg.P2P.Fallback,
		},
		PublicDirectAdvertise:       cfg.Direct.Public.Advertise,
		NetworkMode:                 cfg.Network.Mode,
		IsAutostart:                 a.bridge.IsAutoStart(),
		MinimizeToTray:              cfg.IsMinimizeToTray(),
		StartMinimized:              cfg.GUI.StartMinimized,
		Theme:                       cfg.GUI.Theme,
		VerificationPopupTimeoutSec: cfg.VerificationPopupTimeout(),
		Version:                     Version,
		Network: map[string]any{
			"mode":              cfg.Network.Mode,
			"exclude_processes": cfg.Network.ExcludeProcesses,
		},
		NetworkCapabilities: divert.PlatformCapabilities(),
		Revision:            state.Revision,
		RestartRequired:     state.RestartRequired,
		RestartFields:       state.RestartFields,
		ReloadPending:       state.ReloadPending,
		Runtime: map[string]any{
			"serverAddress": state.Runtime.Server.Address,
			"quicPort":      state.Runtime.Server.QUICPort,
			"tcpPort":       state.Runtime.Server.TCPPort,
			"tlsEnabled":    state.Runtime.IsServerTLSEnabled(),
			"insecureTls":   state.Runtime.Server.InsecureTLS,
			"transport":     state.Runtime.Transport.Mode,
			"networkMode":   state.Runtime.Network.Mode,
			"p2p": map[string]any{
				"enabled": state.Runtime.P2P.Enabled == nil || *state.Runtime.P2P.Enabled, "mode": state.Runtime.P2P.Mode,
				"punchTimeoutMs": state.Runtime.P2P.PunchTimeoutMs, "keepaliveSec": state.Runtime.P2P.KeepaliveSec,
				"idleTimeoutSec": state.Runtime.P2P.IdleTimeoutSec, "maxExitSessions": state.Runtime.P2P.MaxExitSessions,
				"fallback": state.Runtime.P2P.Fallback == nil || *state.Runtime.P2P.Fallback,
			},
			"socks5": proxyLeg{
				Enabled: state.Runtime.Proxy.SOCKS5.Enabled == nil || *state.Runtime.Proxy.SOCKS5.Enabled,
				Listen:  state.Runtime.Proxy.SOCKS5.Listen,
				Port:    state.Runtime.Proxy.SOCKS5.Port,
			},
			"http": proxyLeg{
				Enabled: state.Runtime.Proxy.HTTP.Enabled == nil || *state.Runtime.Proxy.HTTP.Enabled,
				Listen:  state.Runtime.Proxy.HTTP.Listen,
				Port:    state.Runtime.Proxy.HTTP.Port,
			},
		},
		Routing: map[string]any{
			"mode":           cfg.Routing.Mode,
			"default_action": cfg.Routing.DefaultAction,
			"rules":          cfg.Routing.Rules,
		},
	}
	payload.SOCKS5 = proxyLeg{
		Enabled: cfg.Proxy.SOCKS5.Enabled == nil || *cfg.Proxy.SOCKS5.Enabled,
		Listen:  cfg.Proxy.SOCKS5.Listen,
		Port:    cfg.Proxy.SOCKS5.Port,
	}
	payload.HTTP = proxyLeg{
		Enabled: cfg.Proxy.HTTP.Enabled == nil || *cfg.Proxy.HTTP.Enabled,
		Listen:  cfg.Proxy.HTTP.Listen,
		Port:    cfg.Proxy.HTTP.Port,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "{}", nil
	}
	return string(data), nil
}

func (s *WailsService) SaveConfig(raw string) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return saveResponse(nil, nil), nil
	}
	var in bridge.ConfigUpdate
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return saveResponse(nil, err), nil
	}
	res, err := s.owner.bridge.SaveConfig(in)
	if err != nil {
		return saveResponse(res, err), nil
	}
	if in.GUI.Theme != nil {
		s.owner.applyThemeMode(*in.GUI.Theme)
	}
	if in.GUI.MinimizeToTray != nil {
		s.owner.mu.Lock()
		s.owner.minimizeTray = *in.GUI.MinimizeToTray
		s.owner.mu.Unlock()
	}
	if in.GUI.VerificationPopupTimeoutSec != nil {
		s.owner.setVerificationPopupTimeout(*in.GUI.VerificationPopupTimeoutSec)
	}
	return saveResponse(res, nil), nil
}

func (s *WailsService) ReloadConfig() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return saveResponse(nil, nil), nil
	}
	res, err := s.owner.bridge.ReloadConfig()
	if err == nil {
		cfg := s.owner.bridge.GetConfig()
		s.owner.mu.Lock()
		s.owner.minimizeTray = cfg.IsMinimizeToTray()
		s.owner.mu.Unlock()
		s.owner.applyThemeMode(cfg.GUI.Theme)
		s.owner.setVerificationPopupTimeout(cfg.VerificationPopupTimeout())
	}
	return saveResponse(res, err), nil
}

func (s *WailsService) SelectExit(exitID string) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "GUI unavailable", nil
	}
	if err := s.owner.bridge.SelectExit(exitID); err != nil {
		return err.Error(), nil
	}
	return okResult, nil
}

func (s *WailsService) GetNetworkServiceStatus() (string, error) {
	data, err := json.Marshal(divert.GetPlatformServiceStatus())
	if err != nil {
		return "{}", nil
	}
	return string(data), nil
}

func (s *WailsService) RepairNetworkService() (string, error) {
	if err := divert.RepairPlatformService(); err != nil {
		log.Printf("[GUI] 修复 Network Service 失败: %v", err)
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	data, _ := json.Marshal(map[string]any{"ok": true, "message": "Network Service 已安装/修复并启动"})
	return string(data), nil
}

func (s *WailsService) UninstallNetworkService() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": "GUI 尚未连接到 Agent"})
		return string(data), nil
	}
	// Ask for elevation and remove the privileged component first. A cancelled
	// UAC prompt must not silently alter the user's saved transparent-proxy
	// configuration.
	uninstallResult, err := divert.UninstallPlatformService()
	if err != nil {
		log.Printf("[GUI] 卸载 Network Service 失败: %v", err)
		data, _ := json.Marshal(map[string]any{"ok": false, "message": "卸载服务失败，透明代理配置保持不变：" + err.Error()})
		return string(data), nil
	}

	// Make uninstall durable. Leaving network.mode=divert saved would cause the
	// next interactive GUI launch to reinstall the broker immediately.
	disabled := ""
	var update bridge.ConfigUpdate
	update.Network.Mode = &disabled
	if _, err := s.owner.bridge.SaveConfig(update); err != nil {
		log.Printf("[GUI] Network Service 已卸载，但关闭透明代理配置失败: %v", err)
		data, _ := json.Marshal(map[string]any{
			"ok":      false,
			"message": "Network Service 已卸载，但关闭透明代理配置失败；下次启动可能再次请求安装：" + err.Error(),
		})
		return string(data), nil
	}
	message := "Network Service 已完全卸载，系统透明代理配置已关闭"
	if uninstallResult.RebootCleanup {
		message = "Network Service 已从 SCM 卸载；部分 ProgramData 文件仍被 Windows 占用，已安排重启后删除：" + uninstallResult.CleanupPath
	}
	data, _ := json.Marshal(map[string]any{
		"ok":            true,
		"message":       message,
		"rebootCleanup": uninstallResult.RebootCleanup,
		"cleanupPath":   uninstallResult.CleanupPath,
	})
	return string(data), nil
}

func (s *WailsService) SetAutostart(enabled bool) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	if err := s.owner.bridge.SetAutoStart(enabled); err != nil {
		log.Printf("[GUI] 设置开机自启失败: %v", err)
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	return `{"ok":true}`, nil
}

func (s *WailsService) SetTheme(theme string) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "GUI unavailable", nil
	}
	theme = strings.ToLower(strings.TrimSpace(theme))
	switch theme {
	case "light", "dark", "system":
	default:
		theme = "system"
	}
	var in bridge.ConfigUpdate
	in.GUI.Theme = &theme
	if _, err := s.owner.bridge.SaveConfig(in); err != nil {
		return err.Error(), nil
	}
	s.owner.applyThemeMode(theme)
	return okResult, nil
}

func (s *WailsService) CopyClipboard(text string) {
	if s != nil && s.owner != nil && s.owner.app != nil {
		s.owner.app.Clipboard.SetText(text)
	}
}

func (s *WailsService) OpenConfigDir() {
	if s != nil && s.owner != nil && s.owner.bridge != nil {
		openDirectory(filepath.Dir(s.owner.bridge.ConfigPath()))
	}
}

func (s *WailsService) MinimizeWindow() {
	if s != nil && s.owner != nil {
		s.owner.showWindow(false)
	}
}

func (s *WailsService) Restart() (string, error) {
	if err := RequestRestart(); err != nil {
		log.Printf("[GUI] 重启客户端失败: %v", err)
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	return `{"ok":true}`, nil
}

func (s *WailsService) Quit() {
	log.Println("[GUI] 用户请求退出客户端")
	RequestQuit()
}

func saveResponse(res *bridge.SaveResult, err error) string {
	if err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data)
	}
	if res == nil {
		return `{"ok":false,"message":"GUI unavailable"}`
	}
	data, _ := json.Marshal(res)
	return string(data)
}

func rdpActionResponse(value map[string]any, err error) string {
	if err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data)
	}
	if value == nil {
		value = make(map[string]any)
	}
	value["ok"] = true
	data, _ := json.Marshal(value)
	return string(data)
}
