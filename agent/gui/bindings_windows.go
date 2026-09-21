//go:build windows

package gui

import (
	"encoding/json"
	"log"
	"path/filepath"
	"strings"

	"relayproxy/agent/bridge"
	"relayproxy/agent/divert"
	"relayproxy/internal/protocol"
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

func (s *WailsService) GetStatus() (string, error) {
	if s == nil || s.owner == nil {
		return "{}", nil
	}
	return s.owner.statusJSON(), nil
}

func (s *WailsService) GetRemoteDesktopTargets() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "[]", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetRemoteDesktopTargets())
	if err != nil {
		return "[]", nil
	}
	return string(data), nil
}

func (s *WailsService) ConnectRemoteDesktop(targetID string, rawOptions string) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	var options protocol.RemoteDesktopConnectOptions
	if strings.TrimSpace(rawOptions) != "" {
		if err := json.Unmarshal([]byte(rawOptions), &options); err != nil {
			data, _ := json.Marshal(map[string]any{"ok": false, "message": "invalid remote desktop options: " + err.Error()})
			return string(data), nil
		}
	}
	session, err := s.owner.bridge.ConnectRemoteDesktop(targetID, options)
	if err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	data, _ := json.Marshal(map[string]any{"ok": true, "session": session})
	return string(data), nil
}

func (s *WailsService) DisconnectRemoteDesktop() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	s.owner.bridge.DisconnectRemoteDesktop()
	return `{"ok":true}`, nil
}

func (s *WailsService) GetRemoteDesktopStatus() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"state":"idle"}`, nil
	}
	data, err := json.Marshal(s.owner.bridge.GetRemoteDesktopStatus())
	if err != nil {
		return `{"state":"idle"}`, nil
	}
	return string(data), nil
}

func (s *WailsService) GetRemoteDesktopFrame() (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return "{}", nil
	}
	data, err := json.Marshal(s.owner.bridge.GetRemoteDesktopFrame())
	if err != nil {
		return "{}", nil
	}
	return string(data), nil
}

func (s *WailsService) SendRemoteDesktopInput(rawEvent string) (string, error) {
	if s == nil || s.owner == nil || s.owner.bridge == nil {
		return `{"ok":false,"message":"GUI unavailable"}`, nil
	}
	var event protocol.DesktopInputEvent
	if err := json.Unmarshal([]byte(rawEvent), &event); err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": "invalid remote desktop input: " + err.Error()})
		return string(data), nil
	}
	if err := s.owner.bridge.SendRemoteDesktopInput(event); err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data), nil
	}
	return `{"ok":true}`, nil
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
		ConfigPath          string              `json:"configPath"`
		ServerAddress       string              `json:"serverAddress"`
		QUICPort            int                 `json:"quicPort"`
		TCPPort             int                 `json:"tcpPort"`
		TLSEnabled          bool                `json:"tlsEnabled"`
		DeviceName          string              `json:"deviceName"`
		Transport           string              `json:"transport"`
		SOCKS5              proxyLeg            `json:"socks5"`
		HTTP                proxyLeg            `json:"http"`
		DefaultExitID       string              `json:"defaultExitId"`
		ExitEnabled         bool                `json:"exitEnabled"`
		AllowInternet       bool                `json:"allowInternet"`
		AllowPrivate        bool                `json:"allowPrivateNetwork"`
		AllowLoopback       bool                `json:"allowLoopback"`
		AccessMode          string              `json:"accessMode"`
		AccessDomains       []string            `json:"accessDomains"`
		AccessCIDRs         []string            `json:"accessCidrs"`
		NetworkMode         string              `json:"networkMode"`
		IsAutostart         bool                `json:"isAutostart"`
		MinimizeToTray      bool                `json:"minimizeToTray"`
		StartMinimized      bool                `json:"startMinimized"`
		Theme               string              `json:"theme"`
		Version             string              `json:"version"`
		Routing             any                 `json:"routing"`
		Network             any                 `json:"network"`
		NetworkCapabilities divert.Capabilities `json:"networkCapabilities"`
		Runtime             any                 `json:"runtime"`
		Revision            string              `json:"revision"`
		RestartRequired     bool                `json:"restartRequired"`
		RestartFields       []string            `json:"restartFields"`
		ReloadPending       bool                `json:"reloadPending"`
	}{
		ConfigPath:     a.bridge.ConfigPath(),
		ServerAddress:  cfg.Server.Address,
		QUICPort:       cfg.Server.QUICPort,
		TCPPort:        cfg.Server.TCPPort,
		TLSEnabled:     cfg.IsServerTLSEnabled(),
		DeviceName:     cfg.Device.Name,
		Transport:      cfg.Transport.Mode,
		DefaultExitID:  cfg.Proxy.DefaultExitID,
		ExitEnabled:    cfg.Exit.Enabled == nil || *cfg.Exit.Enabled,
		AllowInternet:  cfg.Exit.AllowInternet,
		AllowPrivate:   cfg.Exit.AllowPrivateNetwork,
		AllowLoopback:  cfg.Exit.AllowLoopback,
		AccessMode:     cfg.Exit.Access.Mode,
		AccessDomains:  cfg.Exit.Access.Domains,
		AccessCIDRs:    cfg.Exit.Access.CIDRs,
		NetworkMode:    cfg.Network.Mode,
		IsAutostart:    a.bridge.IsAutoStart(),
		MinimizeToTray: cfg.IsMinimizeToTray(),
		StartMinimized: cfg.GUI.StartMinimized,
		Theme:          cfg.GUI.Theme,
		Version:        Version,
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
			"transport":     state.Runtime.Transport.Mode,
			"networkMode":   state.Runtime.Network.Mode,
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
