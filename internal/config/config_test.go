package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/agent/routing"
)

func TestApplyAgentDefaultsPreservesExplicitRoutingMode(t *testing.T) {
	cfg := &AgentConfigFile{}
	cfg.Routing.Mode = routing.ModeRule
	cfg.Routing.DefaultAction = routing.ActionDirect
	cfg.Routing.Rules = nil

	applyAgentDefaults(cfg)

	if cfg.Routing.Mode != routing.ModeRule {
		t.Errorf("Mode = %q, want %q (must not be overwritten by DefaultConfig)", cfg.Routing.Mode, routing.ModeRule)
	}
	if cfg.Routing.DefaultAction != routing.ActionDirect {
		t.Errorf("DefaultAction = %q, want %q", cfg.Routing.DefaultAction, routing.ActionDirect)
	}
	if len(cfg.Routing.Rules) != 0 {
		t.Errorf("explicit fallback must not receive a default MATCH/PROXY rule")
	}
}

func TestIdentityIDRequiresGeneratedLetterDigitFormat(t *testing.T) {
	if !validIdentityID("a1b2c3d4e5f6g7h8") {
		t.Fatal("valid generated identity id was rejected")
	}
	for _, value := range []string{
		"team-a", "abcdefghijklmnop", "1234567890123456", "A1b2c3d4e5f6g7h8", "a1b2c3d4e5f6g7-8",
	} {
		if validIdentityID(value) {
			t.Fatalf("invalid identity id %q was accepted", value)
		}
	}
}

func TestNormalizedDefaultsAreConcreteAndNeverPersistAsNull(t *testing.T) {
	agent := &AgentConfigFile{}
	if err := NormalizeAgentConfig(agent); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]*bool{
		"server TLS": agent.Server.TLSEnabled, "SOCKS5": agent.Proxy.SOCKS5.Enabled,
		"HTTP": agent.Proxy.HTTP.Enabled, "RDP": agent.RDP.Enabled,
		"P2P": agent.P2P.Enabled, "P2P fallback": agent.P2P.Fallback,
		"exit": agent.Exit.Enabled, "GUI": agent.GUI.Enabled,
		"minimize to tray": agent.GUI.MinimizeToTray, "web": agent.Web.Enabled,
	} {
		if value == nil || !*value {
			t.Errorf("%s default = %v, want concrete true", name, value)
		}
	}
	if agent.Exit.Access.Domains == nil || agent.Exit.Access.CIDRs == nil {
		t.Fatal("agent access-list defaults must be concrete empty lists")
	}
	if agent.P2P.Mode != "auto" || agent.P2P.PunchTimeoutMs != 1200 || agent.P2P.KeepaliveSec != 10 ||
		agent.P2P.IdleTimeoutSec != 120 || agent.P2P.MaxExitSessions != 4 {
		t.Fatalf("unexpected P2P defaults: %+v", agent.P2P)
	}

	agentPath := filepath.Join(t.TempDir(), "agent.yaml")
	if err := SaveAgentConfig(agentPath, agent); err != nil {
		t.Fatal(err)
	}
	agentYAML, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(agentYAML, []byte("null")) {
		t.Fatalf("agent defaults persisted as null:\n%s", agentYAML)
	}

	server := &ServerConfig{}
	if err := NormalizeServerConfig(server); err != nil {
		t.Fatal(err)
	}
	if server.Server.TLSEnabled == nil || !*server.Server.TLSEnabled || server.RDP.Ingress.Enabled == nil || *server.RDP.Ingress.Enabled ||
		server.P2P.Enabled == nil || !*server.P2P.Enabled || server.P2P.LeaseSec != 60 || server.P2P.MaxSessionsPerDevice != 8 ||
		server.Direct.Enabled == nil || !*server.Direct.Enabled {
		t.Fatalf("unexpected concrete server defaults: tls=%v ingress=%v p2p=%+v direct=%+v", server.Server.TLSEnabled, server.RDP.Ingress.Enabled, server.P2P, server.Direct)
	}
	if server.Exit.Enabled == nil || *server.Exit.Enabled || server.Exit.AllowInternet == nil || !*server.Exit.AllowInternet ||
		server.Exit.Upstream.Mode != "direct" || server.Exit.Access.Domains == nil || server.Exit.Access.CIDRs == nil {
		t.Fatalf("unexpected server exit defaults: %+v", server.Exit)
	}
	serverPath := filepath.Join(t.TempDir(), "server.yaml")
	if err := SaveServerConfig(serverPath, server); err != nil {
		t.Fatal(err)
	}
	serverYAML, err := os.ReadFile(serverPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serverYAML, []byte("null")) {
		t.Fatalf("server defaults persisted as null:\n%s", serverYAML)
	}
}

func TestVerificationPopupTimeoutDefaultsAndExplicitZero(t *testing.T) {
	cfg := &AgentConfigFile{}
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.VerificationPopupTimeout(); got != 15 {
		t.Fatalf("default verification popup timeout=%d, want 15", got)
	}

	zero := 0
	cfg.GUI.VerificationPopupTimeoutSec = &zero
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.VerificationPopupTimeout(); got != 0 {
		t.Fatalf("explicit zero timeout=%d, want 0", got)
	}

	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.VerificationPopupTimeout(); got != 0 {
		t.Fatalf("persisted zero timeout=%d, want 0", got)
	}

	tooLarge := 3601
	loaded.GUI.VerificationPopupTimeoutSec = &tooLarge
	if err := NormalizeAgentConfig(loaded); err == nil {
		t.Fatal("verification popup timeout above 3600 accepted")
	}
}

func TestExitUpstreamDefaultsAndValidation(t *testing.T) {
	cfg := &AgentConfigFile{}
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Exit.Upstream.Mode != "direct" {
		t.Fatalf("default exit upstream mode=%q", cfg.Exit.Upstream.Mode)
	}

	cfg.Exit.Upstream.Mode = "socks5"
	cfg.Exit.Upstream.Address = "127.0.0.1:7890"
	cfg.Exit.Upstream.Username = "user"
	cfg.Exit.Upstream.Password = "secret"
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatalf("valid SOCKS5 upstream rejected: %v", err)
	}

	badMode := *cfg
	badMode.Exit.Upstream.Mode = "wireguard"
	if err := NormalizeAgentConfig(&badMode); err == nil {
		t.Fatal("invalid upstream mode accepted")
	}

	badAddress := *cfg
	badAddress.Exit.Upstream.Mode = "http"
	badAddress.Exit.Upstream.Address = "proxy.example"
	if err := NormalizeAgentConfig(&badAddress); err == nil {
		t.Fatal("upstream without port accepted")
	}

	selfLoop := *cfg
	selfLoop.Exit.Upstream.Mode = "socks5"
	selfLoop.Exit.Upstream.Address = "127.0.0.1:1080"
	if err := NormalizeAgentConfig(&selfLoop); err == nil {
		t.Fatal("exit upstream accepted RelayProxy's own SOCKS5 listener")
	}
}

func TestRoutingEmptyListSurvivesPersistence(t *testing.T) {
	for _, action := range []routing.Action{routing.ActionReject, routing.ActionDirect} {
		t.Run(string(action), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			cfg := &AgentConfigFile{Routing: routing.Config{Mode: routing.ModeRule, DefaultAction: action, Rules: []routing.Rule{}}}
			if err := SaveAgentConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadAgentConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Routing.Mode != routing.ModeRule || loaded.Routing.DefaultAction != action || len(loaded.Routing.Rules) != 0 {
				t.Fatalf("persisted fallback changed: %+v", loaded.Routing)
			}
		})
	}
}

func TestExplicitEmptyRoutingYAMLDoesNotReceiveSampleRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte("routing:\n  rules: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.Mode != routing.ModeRule || len(cfg.Routing.Rules) != 0 {
		t.Fatalf("explicit empty rules were replaced: %+v", cfg.Routing)
	}
}

func TestInvalidPoliciesCannotOverwriteConfig(t *testing.T) {
	changes := map[string]func(*AgentConfigFile){
		"access mode": func(c *AgentConfigFile) { c.Exit.Access.Mode = "typo" },
		"access CIDR": func(c *AgentConfigFile) { c.Exit.Access.CIDRs = []string{"127.0.0.1/999"} },
		"routing action": func(c *AgentConfigFile) {
			c.Routing.Rules = []routing.Rule{{Enabled: true, Action: "ACCEPT"}}
		},
		"routing port": func(c *AgentConfigFile) {
			c.Routing.Rules = []routing.Rule{{Enabled: true, Ports: []string{"443oops"}, Action: routing.ActionDirect}}
		},
		"compound CIDR": func(c *AgentConfigFile) {
			c.Routing.Rules = []routing.Rule{{Enabled: true, Processes: []string{"*"}, Targets: []string{"10.0.0.0/999"}, Action: routing.ActionDirect}}
		},
		"compound port": func(c *AgentConfigFile) {
			c.Routing.Rules = []routing.Rule{{Enabled: true, Processes: []string{"*"}, Ports: []string{"65536"}, Action: routing.ActionDirect}}
		},
		"routing default action": func(c *AgentConfigFile) { c.Routing.DefaultAction = "ACCEPT" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent.yaml")
			cfg := &AgentConfigFile{}
			if err := SaveAgentConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			change(cfg)
			if err := SaveAgentConfig(path, cfg); err == nil {
				t.Fatal("invalid policy was saved")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed save changed the original file")
			}
		})
	}
}

func TestAgentLoadValidatesInactivePolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	data := "mode: CLIENT\nnetwork:\n  mode: ''\nexit:\n  enabled: false\n  access:\n    mode: allow\n    cidrs: ['127.0.0.1/999']\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentConfig(path); err == nil {
		t.Fatal("invalid dormant ACL was accepted")
	}
}

func TestAtomicReplacementFailureLeavesDestinationAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentConfig(path, &AgentConfigFile{}); err == nil {
		t.Fatal("replacing a directory unexpectedly succeeded")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "original" {
		t.Fatalf("destination damaged: %q, %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v, %v", entries, err)
	}
}

func TestConfigRevisionMatchesSavedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := &AgentConfigFile{}
	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, revision, err := LoadAgentConfigWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	if revision != AgentConfigRevision(loaded) {
		t.Fatal("saved and read revisions disagree")
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, []byte("\n# external edit\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	_, edited, err := LoadAgentConfigWithRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	if revision == edited {
		t.Fatal("external edit did not change revision")
	}
}

func TestRelayACLDefaultsAndExplicitPrivateAccess(t *testing.T) {
	for _, tc := range []struct {
		name, yaml        string
		internet, private bool
	}{
		{"default", "", true, false},
		{"private", "relay_acl:\n  allow_private_network: true\n", true, true},
		{"no internet", "relay_acl:\n  allow_internet: false\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServerConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.RelayPolicy()
			if p.AllowInternet != tc.internet || p.AllowPrivateNetwork != tc.private || p.AllowLoopback {
				t.Fatalf("unexpected relay policy: %+v", p)
			}
		})
	}
}

func TestInvalidRelayACLRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("relay_acl:\n  access:\n    mode: allow\n    cidrs: [bad]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerConfig(path); err == nil {
		t.Fatal("invalid relay ACL was accepted")
	}
}

func TestServerConfigLoadsRDPSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	data := `rdp:
  lease_sec: 45
  rendezvous_listen: ":3478"
  rendezvous_advertise: "relay.example.com:3478"
  ingress:
    enabled: true
    listen: "0.0.0.0:20100"
    port_start: 20100
    port_end: 20200
    source_cidrs: ["203.0.113.0/24"]
    rate_limit_per_minute: 60
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("RDP server config was rejected: %v", err)
	}
	if cfg.RDP.LeaseSec != 45 || cfg.RDP.RendezvousListen != ":3478" || cfg.RDP.RendezvousAdvertise != "relay.example.com:3478" {
		t.Fatalf("RDP rendezvous settings were not loaded: %+v", cfg.RDP)
	}
	if cfg.RDP.Ingress.Enabled == nil || !*cfg.RDP.Ingress.Enabled || cfg.RDP.Ingress.Listen != "0.0.0.0:20100" {
		t.Fatalf("RDP ingress settings were not loaded: %+v", cfg.RDP.Ingress)
	}
	if len(cfg.RDP.Ingress.SourceCIDRs) != 1 || cfg.RDP.Ingress.SourceCIDRs[0] != "203.0.113.0/24" {
		t.Fatalf("RDP ingress CIDRs were not loaded: %+v", cfg.RDP.Ingress.SourceCIDRs)
	}
}

func TestApplyAgentDefaultsFillsEmptyRouting(t *testing.T) {
	cfg := &AgentConfigFile{}
	applyAgentDefaults(cfg)

	if cfg.Routing.Mode != routing.ModeGlobalProxy {
		t.Errorf("Mode = %q, want %q", cfg.Routing.Mode, routing.ModeGlobalProxy)
	}
	if cfg.Routing.DefaultAction != routing.ActionProxy {
		t.Errorf("DefaultAction = %q, want %q", cfg.Routing.DefaultAction, routing.ActionProxy)
	}
	if len(cfg.Routing.Rules) == 0 {
		t.Errorf("expected default rules")
	}
}

func TestWebManagementDefaultsAndRemoteListenAllowsEmptyToken(t *testing.T) {
	cfg := &AgentConfigFile{}
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsWebEnabled() || cfg.Web.Listen != "127.0.0.1" || cfg.Web.Port != 9090 {
		t.Fatalf("unexpected web defaults: %+v", cfg.Web)
	}
	if cfg.GUI.Theme != "system" {
		t.Fatalf("default GUI theme = %q, want system", cfg.GUI.Theme)
	}
	for _, listen := range []string{"0.0.0.0", "::", "192.168.1.10"} {
		cfg.Web.Listen = listen
		cfg.Web.Token = ""
		if err := NormalizeAgentConfig(cfg); err != nil {
			t.Fatalf("web.listen %q without token was rejected: %v", listen, err)
		}
	}
	cfg.Web.Token = strings.Repeat("t", 31)
	if err := NormalizeAgentConfig(cfg); err == nil {
		t.Fatal("short non-empty web.token was accepted")
	}
	cfg.Web.Token += "t"
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatalf("32-byte web.token rejected: %v", err)
	}
}

func TestAgentThemeAllowsSystem(t *testing.T) {
	cfg := &AgentConfigFile{}
	cfg.GUI.Theme = "system"
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatalf("system theme rejected: %v", err)
	}
}

func TestServerConfigLoadsP2PSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	data := `p2p:
  enabled: true
  rendezvous_listen: ":3479"
  rendezvous_advertise: "relay.example.com:3479"
  lease_sec: 45
  max_sessions_per_device: 12
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadServerConfig(path)
	if err != nil {
		t.Fatalf("P2P server config was rejected: %v", err)
	}
	if cfg.P2P.Enabled == nil || !*cfg.P2P.Enabled || cfg.P2P.RendezvousListen != ":3479" ||
		cfg.P2P.RendezvousAdvertise != "relay.example.com:3479" || cfg.P2P.LeaseSec != 45 ||
		cfg.P2P.MaxSessionsPerDevice != 12 {
		t.Fatalf("P2P settings were not loaded: %+v", cfg.P2P)
	}
}

func TestServerConfigRejectsSharedRendezvousSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	data := `rdp:
  rendezvous_listen: ":3478"
p2p:
  enabled: true
  rendezvous_listen: ":3478"
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerConfig(path); err == nil {
		t.Fatal("shared RDP/P2P rendezvous socket was accepted")
	}
}

func TestAgentPublicDirectAdvertiseValidateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := &AgentConfigFile{}
	cfg.Direct.Public.Advertise = " exit.example.com:35820 "
	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Direct.Public.Advertise != "exit.example.com:35820" {
		t.Fatalf("public direct advertise=%q", loaded.Direct.Public.Advertise)
	}

	loaded.Direct.Public.Advertise = "exit.example.com"
	if err := ValidateAgentConfig(loaded); err == nil {
		t.Fatal("public direct advertise without port was accepted")
	}
}

func TestAgentP2PSettingsValidateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	cfg := &AgentConfigFile{}
	cfg.P2P.Mode = "p2p_only"
	cfg.P2P.PunchTimeoutMs = 800
	cfg.P2P.KeepaliveSec = 15
	cfg.P2P.IdleTimeoutSec = 180
	cfg.P2P.MaxExitSessions = 6
	cfg.P2P.Enabled = BoolPtr(true)
	cfg.P2P.Fallback = BoolPtr(false)
	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.P2P.Mode != "p2p_only" || loaded.P2P.PunchTimeoutMs != 800 ||
		loaded.P2P.KeepaliveSec != 15 || loaded.P2P.IdleTimeoutSec != 180 ||
		loaded.P2P.MaxExitSessions != 6 || loaded.P2P.Fallback == nil || *loaded.P2P.Fallback {
		t.Fatalf("P2P settings changed after persistence: %+v", loaded.P2P)
	}
	directOnly := *loaded
	directOnly.P2P.Mode = "direct_only"
	if err := ValidateAgentConfig(&directOnly); err != nil {
		t.Fatalf("direct_only mode was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*AgentConfigFile){
		"mode":          func(c *AgentConfigFile) { c.P2P.Mode = "magic" },
		"punch timeout": func(c *AgentConfigFile) { c.P2P.PunchTimeoutMs = 10 },
		"keepalive":     func(c *AgentConfigFile) { c.P2P.KeepaliveSec = 1 },
		"idle timeout":  func(c *AgentConfigFile) { c.P2P.IdleTimeoutSec = 5 },
		"max sessions":  func(c *AgentConfigFile) { c.P2P.MaxExitSessions = 0; c.P2P.Mode = "auto" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := *loaded
			mutate(&bad)
			if name == "max sessions" {
				bad.P2P.MaxExitSessions = 33
			}
			if err := ValidateAgentConfig(&bad); err == nil {
				t.Fatalf("invalid P2P %s accepted", name)
			}
		})
	}
}

func TestServerPublicDirectPortRangeValidation(t *testing.T) {
	cfg := &ServerConfig{}
	if err := NormalizeServerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Direct.Enabled == nil || !*cfg.Direct.Enabled {
		t.Fatalf("default Public Direct enabled=%v, want true", cfg.Direct.Enabled)
	}
	if cfg.Direct.PortStart != 0 || cfg.Direct.PortEnd != 0 {
		t.Fatalf("default Public Direct port range=%d-%d, want OS-assigned 0-0", cfg.Direct.PortStart, cfg.Direct.PortEnd)
	}

	cfg.Direct.PortStart, cfg.Direct.PortEnd = 31000, 31100
	if err := NormalizeServerConfig(cfg); err != nil {
		t.Fatalf("valid Public Direct port range rejected: %v", err)
	}

	cfg.Direct.PortStart, cfg.Direct.PortEnd = 31000, 0
	if err := NormalizeServerConfig(cfg); err == nil {
		t.Fatal("partial Public Direct port range was accepted")
	}

	cfg.Direct.PortStart, cfg.Direct.PortEnd = 31100, 31000
	if err := NormalizeServerConfig(cfg); err == nil {
		t.Fatal("reversed Public Direct port range was accepted")
	}
}

func TestServerP2PPortRangeValidation(t *testing.T) {
	cfg := &ServerConfig{}
	if err := NormalizeServerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.P2P.PortStart != 0 || cfg.P2P.PortEnd != 0 {
		t.Fatalf("default P2P port range=%d-%d, want OS-assigned 0-0", cfg.P2P.PortStart, cfg.P2P.PortEnd)
	}
	if cfg.P2P.UPnPEnabled == nil || *cfg.P2P.UPnPEnabled {
		t.Fatalf("default P2P UPnP=%v, want disabled", cfg.P2P.UPnPEnabled)
	}

	cfg.P2P.PortStart, cfg.P2P.PortEnd = 30000, 30100
	if err := NormalizeServerConfig(cfg); err != nil {
		t.Fatalf("valid P2P port range rejected: %v", err)
	}

	cfg.P2P.PortStart, cfg.P2P.PortEnd = 30000, 0
	if err := NormalizeServerConfig(cfg); err == nil {
		t.Fatal("partial P2P port range was accepted")
	}

	cfg.P2P.PortStart, cfg.P2P.PortEnd = 30100, 30000
	if err := NormalizeServerConfig(cfg); err == nil {
		t.Fatal("reversed P2P port range was accepted")
	}
}
