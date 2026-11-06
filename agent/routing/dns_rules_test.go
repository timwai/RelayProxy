package routing

import (
	"strings"
	"testing"
)

func TestDNSRulesSelectExitByDomainBeforeLookup(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProxyDNSEnabled = true
	cfg.DNSRules = []DNSRule{
		{Name: "US", Enabled: true, Domains: []string{"*.google.com", "openai.com"}, Action: DNSActionRemote, ExitID: "us-exit"},
		{Name: "office", Enabled: true, Domains: []string{"*.corp.example"}, Action: DNSActionRemote, ExitID: "office-exit"},
		{Name: "local", Enabled: true, Domains: []string{"local.example"}, Action: DNSActionLocal},
	}
	e, err := newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host  string
		local bool
		exit  string
	}{
		{"google.com", false, "us-exit"},
		{"API.Google.Com.", false, "us-exit"},
		{"openai.com", false, "us-exit"},
		{"api.openai.com", true, ""},
		{"evilgoogle.com", true, ""},
		{"google.com.attacker.test", true, ""},
		{"host.corp.example", false, "office-exit"},
		{"local.example", true, ""},
		{"www.baidu.com", true, ""},
	} {
		local, exit := e.DNSRoute(tc.host)
		if local != tc.local || exit != tc.exit {
			t.Errorf("%q => local=%v, exit=%q; want local=%v, exit=%q", tc.host, local, exit, tc.local, tc.exit)
		}
	}
	// A DNS rule only affects DNS queries, never application routing decisions.
	decision := e.DecideFlow(Flow{Host: "google.com", Port: 443, Protocol: "tcp"})
	if decision.ExitID == "us-exit" {
		t.Fatal("DNS-only exit unexpectedly changed the application connection")
	}
}

func TestDNSRulesLegacyModeAndDisabledRule(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProxyDNSEnabled = true
	e, err := newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if local, id := e.DNSRoute("example.com"); local || id != "" {
		t.Fatal("old configs must retain global encrypted DNS resolution")
	}
	cfg.DNSRules = []DNSRule{{Enabled: false, Action: DNSActionRemote, Domains: []string{"*.example.com"}, ExitID: "unused"}}
	e, err = newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if local, id := e.DNSRoute("example.com"); !local || id != "" {
		t.Fatal("disabled rules must not match; an explicit ruleset defaults to local DNS")
	}
	cfg.ProxyDNSEnabled = false
	e, err = newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if local, id := e.DNSRoute("example.com"); local || id != "" {
		t.Fatal("inactive DNS capture must not apply DNS-specific policy")
	}
}

func TestDNSRulesValidateAndClone(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProxyDNSEnabled = true
	cfg.DNSRules = []DNSRule{{Name: "overseas", Enabled: true, Action: DNSActionRemote, Domains: []string{"*.example.com"}, ExitID: "external"}}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	copy := CloneConfig(cfg)
	copy.DNSRules[0].Domains[0] = "*.altered.com"
	if cfg.DNSRules[0].Domains[0] != "*.example.com" {
		t.Fatal("DNS domain selector was not cloned")
	}
	if ids := DNSRuleExitIDs(cfg.DNSRules); len(ids) != 1 || ids[0] != "external" {
		t.Fatalf("DNS exit references lost: %v", ids)
	}
	for _, tc := range []struct {
		label  string
		mutate func(*Config)
	}{
		{"empty domains", func(c *Config) { c.DNSRules[0].Domains = nil }},
		{"broad wildcard", func(c *Config) { c.DNSRules[0].Domains = []string{"*example.com"} }},
		{"bad IP", func(c *Config) { c.DNSRules[0].Domains = []string{"127.0.0.1"} }},
		{"empty label", func(c *Config) { c.DNSRules[0].Domains = []string{"*.example..com"} }},
		{"unsupported action", func(c *Config) { c.DNSRules[0].Action = "PROXY" }},
		{"local exit", func(c *Config) { c.DNSRules[0].Action = DNSActionLocal }},
		{"bad exit", func(c *Config) { c.DNSRules[0].ExitID = "bad exit" }},
	} {
		t.Run(tc.label, func(t *testing.T) {
			c := CloneConfig(cfg)
			tc.mutate(&c)
			if err := ValidateConfig(c); err == nil || !strings.Contains(err.Error(), "dns_rules") {
				t.Fatalf("invalid rule accepted or wrong error: %v", err)
			}
		})
	}
}
