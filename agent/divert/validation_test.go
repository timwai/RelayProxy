package divert

import (
	"reflect"
	"strings"
	"testing"
)

func TestProcessPathDoesNotWidenToBasename(t *testing.T) {
	tests := []struct {
		pattern, process string
		want             bool
	}{
		{"/opt/trusted/*", "/tmp/untrusted/curl", false},
		{"/opt/trusted/*", "/opt/trusted/curl", true},
		{"/usr/bin/curl", "/opt/bin/curl", false},
		{"curl", "/opt/bin/curl", true},
		{`C:\Trusted\browser.exe`, `C:\Other\browser.exe`, false},
		{`C:\Trusted\*`, `C:\Other\browser.exe`, false},
		{`C:\Trusted\*`, `c:\TRUSTED\browser.exe`, true},
		{`C:/Trusted/browser.exe`, `C:\Trusted\browser.exe`, true},
		{"*.exe", `C:\Other\BROWSER.EXE`, true},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.process, func(t *testing.T) {
			if got := matchProcess(tt.pattern, tt.process); got != tt.want {
				t.Fatalf("matchProcess(%q, %q)=%v, want %v", tt.pattern, tt.process, got, tt.want)
			}
		})
	}
}

func TestInvalidConfigCannotReplaceRunningRules(t *testing.T) {
	good := Config{DefaultAction: ActionReject, Rules: []Rule{{
		Name: "only-web", Enabled: true, Process: "browser.exe", Ports: []string{"443"}, Action: ActionProxy, ExitID: "exit-a",
	}}}
	tests := []struct {
		name string
		edit func(*Config)
	}{
		{"mode", func(c *Config) { c.Mode = "tun" }},
		{"default-action", func(c *Config) { c.DefaultAction = "PROXXY" }},
		{"rule-action", func(c *Config) { c.Rules[0].Action = "DROP" }},
		{"process-glob", func(c *Config) { c.Rules[0].Process = "[" }},
		{"empty-exclusion", func(c *Config) { c.ExcludeProcesses = []string{" "} }},
		{"cidr", func(c *Config) { c.Rules[0].CIDRs = []string{"10.0.0.0/99"} }},
		{"empty-cidr", func(c *Config) { c.Rules[0].CIDRs = []string{""} }},
		{"malformed-port", func(c *Config) { c.Rules[0].Ports = []string{"4433x"} }},
		{"zero-port", func(c *Config) { c.Rules[0].Ports = []string{"0"} }},
		{"large-port", func(c *Config) { c.Rules[0].Ports = []string{"65536"} }},
		{"reverse-range", func(c *Config) { c.Rules[0].Ports = []string{"443-80"} }},
		{"signed-port", func(c *Config) { c.Rules[0].Ports = []string{"+443"} }},
		{"empty-port", func(c *Config) { c.Rules[0].Ports = []string{""} }},
		{"protocol", func(c *Config) { c.Rules[0].Protocols = []string{"icmp"} }},
		{"empty-protocol", func(c *Config) { c.Rules[0].Protocols = []string{""} }},
		{"hostname-glob", func(c *Config) { c.Rules[0].Hosts = []string{"["} }},
		{"hostname-url", func(c *Config) { c.Rules[0].Hosts = []string{"https://example.com"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := requireEngine(t, good)
			before := engine.Config()
			bad := cloneConfig(good)
			tt.edit(&bad)
			bad.Rules[0].Enabled = false // disabled/stored rules are also validated
			if err := ValidateConfig(bad); err == nil {
				t.Fatal("invalid config accepted")
			}
			if _, err := NewEngine(bad); err == nil {
				t.Fatal("invalid engine created")
			}
			if err := engine.Reload(bad); err == nil {
				t.Fatal("invalid config replaced the running policy")
			}
			if !reflect.DeepEqual(before, engine.Config()) {
				t.Fatal("failed reload changed configuration")
			}
			if d := engine.Match(Flow{Process: "browser.exe", Port: 443}); d.Action != ActionProxy || d.ExitID != "exit-a" {
				t.Fatalf("old policy not retained: %+v", d)
			}
		})
	}
}

func TestEngineOwnsConfigurationAndNormalizesValidValues(t *testing.T) {
	cfg := Config{DefaultAction: " direct ", Rules: []Rule{{
		Enabled: true, Process: "app", Protocols: []string{" TCP "}, CIDRs: []string{"10.0.0.0/8", "2001:db8::/32"},
		Ports: []string{"80-443"}, Action: " proxy ", ExitID: "chosen",
	}}}
	engine := requireEngine(t, cfg)
	cfg.Rules[0].CIDRs[0] = "192.168.0.0/16"
	cfg.Rules[0].Protocols[0] = "udp"
	snapshot := engine.Config()
	snapshot.Rules[0].Ports[0] = "1"
	for _, ip := range []string{"10.1.2.3", "::ffff:10.1.2.3", "2001:db8::1"} {
		if got := engine.Match(Flow{Process: "app", IP: ip, Port: 443, Protocol: ProtoTCP}); got.Action != ActionProxy {
			t.Fatalf("configuration aliased/normalization failed for %s: %+v", ip, got)
		}
	}
	if !strings.Contains(cfg.Rules[0].Protocols[0], "udp") || cfg.DefaultAction != " direct " {
		t.Fatal("validation modified caller configuration")
	}
}
