package routing

import (
	"testing"
)

func TestEngine_Mode(t *testing.T) {
	tests := []struct {
		name       string
		mode       Mode
		host       string
		port       uint16
		wantAction Action
	}{
		{
			name:       "ModeGlobalProxy always returns ActionProxy",
			mode:       "global_proxy",
			host:       "example.com",
			port:       80,
			wantAction: "PROXY",
		},
		{
			name:       "ModeDirect always returns ActionDirect",
			mode:       "direct",
			host:       "example.com",
			port:       80,
			wantAction: "DIRECT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Mode: tt.mode,
				Rules: []Rule{
					{Enabled: true, Action: "REJECT"},
				},
			}
			engine, err := NewEngine(cfg)
			if err != nil {
				t.Fatal(err)
			}
			action, _ := engine.Match(tt.host, tt.port)
			if action != tt.wantAction {
				t.Errorf("Match() got action %v, want %v", action, tt.wantAction)
			}
		})
	}
}

func TestEngine_Rules(t *testing.T) {
	tests := []struct {
		name       string
		rules      []Rule
		host       string
		port       uint16
		wantAction Action
		wantExitID string
		wantError  bool
	}{
		{
			name: "DOMAIN exact match",
			rules: []Rule{
				{Enabled: true, Targets: []string{"example.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "example.com",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "DOMAIN exact match case-insensitive",
			rules: []Rule{
				{Enabled: true, Targets: []string{"Example.Com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "example.com",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "DOMAIN mismatch",
			rules: []Rule{
				{Enabled: true, Targets: []string{"example.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "www.example.com",
			port:       80,
			wantAction: "DIRECT", // Default action
		},
		{
			name: "DOMAIN-SUFFIX match",
			rules: []Rule{
				{Enabled: true, Targets: []string{"*.example.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "www.example.com",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "DOMAIN-SUFFIX match exact",
			rules: []Rule{
				{Enabled: true, Targets: []string{"*.example.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "example.com",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "DOMAIN-SUFFIX mismatch edge case",
			rules: []Rule{
				{Enabled: true, Targets: []string{"*.google.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "notgoogle.com",
			port:       80,
			wantAction: "DIRECT",
		},
		{
			name: "DOMAIN-KEYWORD match",
			rules: []Rule{
				{Enabled: true, Targets: []string{"*google*"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "www.google.com",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "IP-CIDR match single IP",
			rules: []Rule{
				{Enabled: true, Targets: []string{"192.168.1.1/32"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "192.168.1.1",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "IP-CIDR match range",
			rules: []Rule{
				{Enabled: true, Targets: []string{"10.0.0.0/8"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "10.1.2.3",
			port:       80,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "PORT exact match",
			rules: []Rule{
				{Enabled: true, Ports: []string{"8080"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "example.com",
			port:       8080,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "MATCH catch-all",
			rules: []Rule{
				{Enabled: true, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "anything.com",
			port:       1234,
			wantAction: "PROXY",
			wantExitID: "exit1",
		},
		{
			name: "First match wins",
			rules: []Rule{
				{Enabled: true, Targets: []string{"example.com"}, Action: "DIRECT", ExitID: ""},
				{Enabled: true, Targets: []string{"*.example.com"}, Action: "PROXY", ExitID: "exit1"},
			},
			host:       "example.com",
			port:       80,
			wantAction: "DIRECT",
			wantExitID: "",
		},
		{
			name: "Disabled rules are skipped",
			rules: []Rule{
				{Enabled: false, Targets: []string{"example.com"}, Action: "PROXY", ExitID: "exit1"},
				{Enabled: true, Action: "DIRECT", ExitID: ""},
			},
			host:       "example.com",
			port:       80,
			wantAction: "DIRECT",
			wantExitID: "",
		},
		{
			name:       "Empty rules list returns default action",
			rules:      []Rule{},
			host:       "example.com",
			port:       80,
			wantAction: "DIRECT", // Config default action
			wantExitID: "",
		},
		{
			name:       "Empty host string",
			rules:      []Rule{{Enabled: true, Targets: []string{"example.com"}, Action: "PROXY"}},
			host:       "",
			port:       80,
			wantAction: "DIRECT",
			wantExitID: "",
		},
		{
			name:      "Invalid CIDR in rule",
			wantError: true,
			rules: []Rule{
				{Enabled: true, Targets: []string{"invalid-cidr/999"}, Action: "PROXY", ExitID: "exit1"},
				{Enabled: true, Action: "DIRECT", ExitID: ""},
			},
			host:       "192.168.1.1",
			port:       80,
			wantAction: "DIRECT",
			wantExitID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Mode:          "rule",
				DefaultAction: "DIRECT",
				Rules:         tt.rules,
			}
			engine, err := NewEngine(cfg)
			if tt.wantError {
				if err == nil {
					t.Fatal("invalid rule was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			action, exitID := engine.Match(tt.host, tt.port)
			if action != tt.wantAction {
				t.Errorf("Match() got action %v, want %v", action, tt.wantAction)
			}
			if exitID != tt.wantExitID {
				t.Errorf("Match() got exitID %v, want %v", exitID, tt.wantExitID)
			}
		})
	}
}

func TestEngine_Reload(t *testing.T) {
	cfg1 := Config{
		Mode:          "rule",
		DefaultAction: "DIRECT",
		Rules: []Rule{
			{Enabled: true, Targets: []string{"example.com"}, Action: "PROXY", ExitID: "exit1"},
		},
	}
	engine, err := NewEngine(cfg1)
	if err != nil {
		t.Fatal(err)
	}
	action, _ := engine.Match("example.com", 80)
	if action != "PROXY" {
		t.Fatalf("Expected PROXY, got %v", action)
	}

	cfg2 := Config{
		Mode:          "rule",
		DefaultAction: "DIRECT",
		Rules: []Rule{
			{Enabled: true, Targets: []string{"example.com"}, Action: "REJECT", ExitID: "exit2"},
		},
	}
	if err := engine.Reload(cfg2); err != nil {
		t.Fatal(err)
	}
	action, exitID := engine.Match("example.com", 80)
	if action != "REJECT" {
		t.Errorf("After reload, expected REJECT, got %v", action)
	}
	if exitID != "exit2" {
		t.Errorf("After reload, expected exit2, got %v", exitID)
	}
}

func TestMatchDomainSuffix(t *testing.T) {
	tests := []struct {
		host   string
		suffix string
		want   bool
	}{
		{"google.com", ".google.com", true},
		{"mail.google.com", ".google.com", true},
		{"notgoogle.com", ".google.com", false},
		{"google.com", "google.com", true},
		{"mail.google.com", "google.com", true},
		{"notgoogle.com", "google.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.host+"_"+tt.suffix, func(t *testing.T) {
			if got := matchDomainSuffix(tt.host, tt.suffix); got != tt.want {
				t.Errorf("matchDomainSuffix(%v, %v) = %v, want %v", tt.host, tt.suffix, got, tt.want)
			}
		})
	}
}
