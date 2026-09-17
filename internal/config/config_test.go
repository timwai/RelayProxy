package config

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestWebManagementDefaultsAndRemoteAuthentication(t *testing.T) {
	cfg := &AgentConfigFile{}
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsWebEnabled() || cfg.Web.Listen != "127.0.0.1" || cfg.Web.Port != 9090 {
		t.Fatalf("unexpected web defaults: %+v", cfg.Web)
	}
	cfg.Web.Listen = "0.0.0.0"
	if err := NormalizeAgentConfig(cfg); err == nil {
		t.Fatal("remote web listener without a token was accepted")
	}
	cfg.Web.Token = "too-short"
	if err := NormalizeAgentConfig(cfg); err == nil {
		t.Fatal("remote web listener with a weak token was accepted")
	}
	cfg.Web.Token = "0123456789abcdef0123456789abcdef"
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatalf("authenticated remote web listener rejected: %v", err)
	}
}
