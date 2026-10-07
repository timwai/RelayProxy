package bridge

import (
	"encoding/json"
	"testing"

	"relayproxy/internal/config"
)

func TestReactConfigPayloadRoundTripsThroughBridge(t *testing.T) {
	b := newTestBridge(t)
	state, err := b.GetConfigState()
	if err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"revision": state.Revision,
		"server": map[string]any{
			"address":     "relay-next.example.test",
			"quicPort":    4433,
			"tcpPort":     8443,
			"tlsEnabled":  true,
			"insecureTls": true,
		},
		"device": map[string]any{
			"name":       "react-agent",
			"identityId": "identity-1234",
		},
		"transport": "auto",
		"p2p": map[string]any{
			"enabled":         true,
			"mode":            "auto",
			"punchTimeoutMs":  1600,
			"keepaliveSec":    12,
			"idleTimeoutSec":  180,
			"maxExitSessions": 6,
			"fallback":        false,
		},
		"direct": map[string]any{
			"public": map[string]any{"advertise": ""},
		},
		"proxy": map[string]any{
			"socks5Enabled": true,
			"socks5Listen":  "127.0.0.1",
			"socks5Port":    1088,
			"httpEnabled":   false,
			"httpListen":    "127.0.0.1",
			"httpPort":      8088,
			"defaultExitId": "exit-react",
		},
		"exit": map[string]any{
			"enabled":             true,
			"allowInternet":       true,
			"allowPrivateNetwork": false,
			"allowLoopback":       false,
			"upstream": map[string]any{
				"mode": "direct",
			},
			"access": map[string]any{
				"mode":    "deny",
				"domains": []string{"*.blocked.test"},
				"cidrs":   []string{"203.0.113.0/24"},
			},
		},
		"network": map[string]any{
			"mode":             "",
			"excludeProcesses": []string{"backup.exe", "updater.exe"},
		},
		"routing": map[string]any{
			"mode":           "direct",
			"default_action": "DIRECT",
			"rules":          []any{},
		},
		"gui": map[string]any{
			"minimizeToTray":              false,
			"startMinimized":              true,
			"theme":                       "light",
			"verificationPopupTimeoutSec": 20,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var update ConfigUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SaveConfig(update); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadAgentConfig(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "relay-next.example.test" || cfg.Server.QUICPort != 4433 || cfg.Server.TCPPort != 8443 || !cfg.Server.InsecureTLS {
		t.Fatalf("server payload mismatch: %+v", cfg.Server)
	}
	if cfg.Device.Name != "react-agent" || cfg.Device.IdentityID != "identity-1234" || cfg.Transport.Mode != "auto" {
		t.Fatalf("device/transport payload mismatch: device=%+v transport=%q", cfg.Device, cfg.Transport.Mode)
	}
	if cfg.P2P.PunchTimeoutMs != 1600 || cfg.P2P.KeepaliveSec != 12 || cfg.P2P.IdleTimeoutSec != 180 || cfg.P2P.MaxExitSessions != 6 || cfg.P2P.Fallback == nil || *cfg.P2P.Fallback {
		t.Fatalf("p2p payload mismatch: %+v", cfg.P2P)
	}
	if cfg.Proxy.SOCKS5.Port != 1088 || cfg.Proxy.HTTP.Port != 8088 || cfg.Proxy.HTTP.Enabled == nil || *cfg.Proxy.HTTP.Enabled || cfg.Proxy.DefaultExitID != "exit-react" {
		t.Fatalf("proxy payload mismatch: %+v", cfg.Proxy)
	}
	if cfg.Exit.Upstream.Mode != "direct" || cfg.Exit.Access.Mode != "deny" || len(cfg.Exit.Access.Domains) != 1 || len(cfg.Exit.Access.CIDRs) != 1 {
		t.Fatalf("exit payload mismatch: %+v", cfg.Exit)
	}
	if cfg.Network.Mode != "" || len(cfg.Network.ExcludeProcesses) != 2 || cfg.Network.ExcludeProcesses[0] != "backup.exe" {
		t.Fatalf("network payload mismatch: %+v", cfg.Network)
	}
	if cfg.Routing.Mode != "direct" || cfg.Routing.DefaultAction != "DIRECT" || len(cfg.Routing.Rules) != 0 {
		t.Fatalf("routing payload mismatch: %+v", cfg.Routing)
	}
	if cfg.IsMinimizeToTray() || !cfg.GUI.StartMinimized || cfg.GUI.Theme != "light" || cfg.VerificationPopupTimeout() != 20 {
		t.Fatalf("gui payload mismatch: %+v", cfg.GUI)
	}
}
