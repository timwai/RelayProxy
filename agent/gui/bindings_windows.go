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

// okResult is the sentinel the UI checks for a successful mutation.
const okResult = "ok"

// registerBindings exposes the Go side to JavaScript as window.go* functions.
//
// Bindings that return strings encode errors in their payload. Configuration
// writes return JSON with an explicit ok flag and pending-runtime state; older
// one-step operations retain the "ok" sentinel.
func (a *appWindow) registerBindings() {
	w := a.w
	if w == nil {
		return
	}

	// --- status & logs -------------------------------------------------------
	_ = w.Bind("goOpenConnections", func() string { a.openConnections(); return okResult })

	_ = w.Bind("goGetStatus", func() (string, error) {
		return a.statusJSON(), nil
	})

	_ = w.Bind("goGetRDPTargets", func() (string, error) {
		data, err := json.Marshal(a.bridge.GetRDPTargets())
		if err != nil {
			return "[]", nil
		}
		return string(data), nil
	})

	_ = w.Bind("goConnectRDP", func(targetID string, autoLaunch bool) (string, error) {
		target, err := a.bridge.ConnectRDP(targetID, autoLaunch)
		if err != nil {
			return saveResponse(nil, err), nil
		}
		data, _ := json.Marshal(map[string]any{"ok": true, "target": target})
		return string(data), nil
	})

	_ = w.Bind("goDisconnectRDP", func() (string, error) {
		a.bridge.DisconnectRDP()
		return `{"ok":true}`, nil
	})

	_ = w.Bind("goGetLogs", func() (string, error) {
		entries := a.bridge.GetLogs(500)
		data, err := json.Marshal(entries)
		if err != nil {
			return "[]", nil
		}
		return string(data), nil
	})

	_ = w.Bind("goClearLogs", func() {
		a.bridge.ClearLogs()
	})

	// --- configuration -------------------------------------------------------

	_ = w.Bind("goGetConfig", func() (string, error) {
		state, err := a.bridge.GetConfigState()
		if err != nil {
			data, _ := json.Marshal(map[string]any{"configError": err.Error(), "configPath": a.bridge.ConfigPath()})
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
			ConfigPath:          a.bridge.ConfigPath(),
			ServerAddress:       cfg.Server.Address,
			QUICPort:            cfg.Server.QUICPort,
			TCPPort:             cfg.Server.TCPPort,
			TLSEnabled:          cfg.IsServerTLSEnabled(),
			DeviceName:          cfg.Device.Name,
			Transport:           cfg.Transport.Mode,
			DefaultExitID:       cfg.Proxy.DefaultExitID,
			ExitEnabled:         cfg.Exit.Enabled == nil || *cfg.Exit.Enabled,
			AllowInternet:       cfg.Exit.AllowInternet,
			AllowPrivate:        cfg.Exit.AllowPrivateNetwork,
			AllowLoopback:       cfg.Exit.AllowLoopback,
			AccessMode:          cfg.Exit.Access.Mode,
			AccessDomains:       cfg.Exit.Access.Domains,
			AccessCIDRs:         cfg.Exit.Access.CIDRs,
			NetworkMode:         cfg.Network.Mode,
			IsAutostart:         a.bridge.IsAutoStart(),
			Theme:               cfg.GUI.Theme,
			Version:             Version,
			Network:             map[string]any{"mode": cfg.Network.Mode, "exclude_processes": cfg.Network.ExcludeProcesses},
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
				"socks5": proxyLeg{Enabled: state.Runtime.Proxy.SOCKS5.Enabled == nil || *state.Runtime.Proxy.SOCKS5.Enabled,
					Listen: state.Runtime.Proxy.SOCKS5.Listen, Port: state.Runtime.Proxy.SOCKS5.Port},
				"http": proxyLeg{Enabled: state.Runtime.Proxy.HTTP.Enabled == nil || *state.Runtime.Proxy.HTTP.Enabled,
					Listen: state.Runtime.Proxy.HTTP.Listen, Port: state.Runtime.Proxy.HTTP.Port},
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
		payload.MinimizeToTray = cfg.IsMinimizeToTray()
		payload.StartMinimized = cfg.GUI.StartMinimized

		data, err := json.Marshal(payload)
		if err != nil {
			return "{}", nil
		}
		return string(data), nil
	})

	_ = w.Bind("goSaveConfig", func(raw string) (string, error) {
		var in bridge.ConfigUpdate
		if err := json.Unmarshal([]byte(raw), &in); err != nil {
			return saveResponse(nil, err), nil
		}
		res, err := a.bridge.SaveConfig(in)
		if err != nil {
			return saveResponse(res, err), nil
		}
		if in.GUI.Theme != nil {
			theme := strings.ToLower(strings.TrimSpace(*in.GUI.Theme))
			a.opts.Theme = theme
			a.applyTheme(theme == "dark")
		}
		if in.GUI.MinimizeToTray != nil {
			a.mu.Lock()
			a.minimizeTray = *in.GUI.MinimizeToTray
			a.mu.Unlock()
		}
		return saveResponse(res, nil), nil
	})

	_ = w.Bind("goReloadConfig", func() (string, error) {
		res, err := a.bridge.ReloadConfig()
		if err == nil {
			cfg := a.bridge.GetConfig()
			a.mu.Lock()
			a.minimizeTray = cfg.IsMinimizeToTray()
			a.mu.Unlock()
			a.opts.Theme = cfg.GUI.Theme
			a.applyTheme(cfg.GUI.Theme == "dark")
		}
		return saveResponse(res, err), nil
	})

	_ = w.Bind("goSelectExit", func(exitID string) (string, error) {
		if err := a.bridge.SelectExit(exitID); err != nil {
			return err.Error(), nil
		}
		return okResult, nil
	})

	// --- preferences & lifecycle --------------------------------------------

	_ = w.Bind("goSetAutostart", func(enabled bool) (string, error) {
		if err := a.bridge.SetAutoStart(enabled); err != nil {
			log.Printf("[GUI] 设置开机自启失败: %v", err)
			out, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
			return string(out), nil
		}
		return `{"ok":true}`, nil
	})

	_ = w.Bind("goSetTheme", func(theme string) (string, error) {
		theme = strings.ToLower(strings.TrimSpace(theme))
		if theme != "light" {
			theme = "dark"
		}
		var in bridge.ConfigUpdate
		in.GUI.Theme = &theme
		if _, err := a.bridge.SaveConfig(in); err != nil {
			return err.Error(), nil
		}
		a.opts.Theme = theme
		a.applyTheme(theme == "dark")
		return okResult, nil
	})

	// --- lifecycle -----------------------------------------------------------

	_ = w.Bind("goCopyClipboard", func(text string) {
		copyToClipboard(text)
	})

	_ = w.Bind("goOpenConfigDir", func() {
		openDirectory(filepath.Dir(a.bridge.ConfigPath()))
	})

	_ = w.Bind("goMinimizeWindow", func() {
		a.showWindow(false)
	})

	_ = w.Bind("goRestart", func() (string, error) {
		if err := RequestRestart(); err != nil {
			log.Printf("[GUI] 重启客户端失败: %v", err)
			out, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
			return string(out), nil
		}
		return `{"ok":true}`, nil
	})

	_ = w.Bind("goQuit", func() {
		log.Println("[GUI] 用户请求退出客户端")
		a.quit()
	})
}

func saveResponse(res *bridge.SaveResult, err error) string {
	if err != nil {
		data, _ := json.Marshal(map[string]any{"ok": false, "message": err.Error()})
		return string(data)
	}
	data, _ := json.Marshal(res)
	return string(data)
}
