package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func testServerSettings(t *testing.T) *ServerSettings {
	t.Helper()
	cfg := &ServerConfig{}
	if err := NormalizeServerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := SaveServerConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	settings, err := NewServerSettings(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestAdminTLSCanDifferFromTunnelTLS(t *testing.T) {
	for _, tunnel := range []bool{false, true} {
		for _, admin := range []*bool{nil, BoolPtr(false), BoolPtr(true)} {
			cfg := &ServerConfig{}
			cfg.Server.TLSEnabled, cfg.Server.Admin.TLSEnabled = BoolPtr(tunnel), admin
			want := tunnel
			if admin != nil {
				want = *admin
			}
			if cfg.IsAdminTLSEnabled() != want || cfg.NeedsCertificate() != (want || tunnel) {
				t.Fatal("listener TLS modes are coupled")
			}
		}
	}
}

func TestListenerAliasesCannotSaveConflictingTCPPorts(t *testing.T) {
	for _, pair := range [][2]string{{":21080", "0.0.0.0:21080"}, {"127.0.0.1:21080", ":21080"}, {"[::]:21080", "0.0.0.0:21080"}, {"localhost:21080", "127.0.0.1:21080"}} {
		cfg := &ServerConfig{}
		cfg.Server.Admin.Listen, cfg.Server.TLS.Listen = pair[0], pair[1]
		if err := NormalizeServerConfig(cfg); err == nil {
			t.Fatalf("conflicting listeners accepted: %v", pair)
		}
	}
	cfg := &AgentConfigFile{}
	cfg.Proxy.SOCKS5.Listen, cfg.Proxy.HTTP.Listen = "0.0.0.0", "127.0.0.1"
	cfg.Proxy.SOCKS5.Port, cfg.Proxy.HTTP.Port = 1080, 1080
	if err := NormalizeAgentConfig(cfg); err == nil {
		t.Fatal("conflicting local proxy sockets accepted")
	}
	cfg.Proxy.HTTP.Enabled = BoolPtr(false)
	if err := NormalizeAgentConfig(cfg); err != nil {
		t.Fatalf("disabled listener should not conflict: %v", err)
	}
}

func TestServerSettingsPersistsWithoutReplacingRuntime(t *testing.T) {
	s := testServerSettings(t)
	initial, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := s.Update(initial.Revision, func(c *ServerConfig) error { c.Server.Admin.TLSEnabled = BoolPtr(false); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if saved.Config.IsAdminTLSEnabled() || !saved.Runtime.IsAdminTLSEnabled() || !saved.Config.IsTLSEnabled() || len(saved.RestartFields) != 1 || !slices.Contains(saved.RestartFields, "server.admin.tls_enabled") {
		t.Fatalf("incorrect desired/runtime state: %+v", saved)
	}
	if saved.Revision == initial.Revision {
		t.Fatal("revision was not updated")
	}
	saved.Runtime.Server.Admin.TLSEnabled = BoolPtr(false)
	*saved.Config.Server.Admin.TLSEnabled = true
	next, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	if !next.Runtime.IsAdminTLSEnabled() || next.Config.IsAdminTLSEnabled() {
		t.Fatal("caller mutated the stored runtime or desired config")
	}
	restarted, err := NewServerSettings(s.path, next.Config)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restarted.State()
	if err != nil || len(afterRestart.RestartFields) != 0 {
		t.Fatalf("restart did not clear pending settings: %v", err)
	}
}

func TestServerSettingsRejectsStaleConcurrentAndInvalidUpdates(t *testing.T) {
	s := testServerSettings(t)
	initial, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, heartbeat := range []int{20, 30} {
		wg.Go(func() {
			_, err := s.Update(initial.Revision, func(c *ServerConfig) error { c.Tunnel.HeartbeatSec = heartbeat; return nil })
			results <- err
		})
	}
	wg.Wait()
	close(results)
	var succeeded, conflicted int
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConfigConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("updates succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	before, _ := os.ReadFile(s.path)
	current, _ := s.State()
	for _, change := range []func(*ServerConfig) error{
		func(c *ServerConfig) error { c.Server.Admin.Listen = "invalid-address"; return nil },
		func(c *ServerConfig) error {
			c.Server.CertFile = filepath.Join(t.TempDir(), "missing.pem")
			c.Server.KeyFile = "missing.key"
			return nil
		},
		func(c *ServerConfig) error {
			c.RelayACL = &RelayACLConfig{Access: AccessConfig{Mode: "allow", CIDRs: []string{"invalid-cidr"}}}
			return nil
		},
	} {
		if _, err := s.Update(current.Revision, change); err == nil {
			t.Fatal("invalid settings accepted")
		}
		after, _ := os.ReadFile(s.path)
		if string(before) != string(after) {
			t.Fatal("validation failure overwrote the configuration")
		}
	}
	s.write = func(string, *ServerConfig) error { return errors.New("disk full") }
	if _, err := s.Update(current.Revision, func(c *ServerConfig) error { c.Tunnel.HeartbeatSec++; return nil }); err == nil {
		t.Fatal("failed write reported success")
	}
	after, _ := s.State()
	if after.Revision != current.Revision || after.Runtime.Tunnel.HeartbeatSec != initial.Runtime.Tunnel.HeartbeatSec {
		t.Fatal("write failure changed the config or runtime")
	}
}
