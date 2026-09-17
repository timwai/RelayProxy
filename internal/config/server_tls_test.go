package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerConfigRejectsIgnoredCertificateFields(t *testing.T) {
	for _, tc := range []struct{ name, yaml, field string }{
		{"camel case", "server:\n  certFile: /data/config/fullchain.pem\n  keyFile: /data/config/privkey.pem\n", "certFile"},
		{"wrong nesting", "server:\n  tls:\n    cert_file: /data/config/fullchain.pem\n    key_file: /data/config/privkey.pem\n", "cert_file"},
		{"unknown option", "server:\n  tls_enable: true\n", "tls_enable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadServerConfig(path); err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("misspelled certificate configuration was ignored: %v", err)
			}
		})
	}
}

func TestServerConfigRequiresCompleteCertificatePair(t *testing.T) {
	for _, yaml := range []string{
		"server:\n  cert_file: cert/server.crt\n",
		"server:\n  key_file: cert/server.key\n",
	} {
		path := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadServerConfig(path); err == nil {
			t.Fatal("incomplete certificate pair was accepted with TLS enabled")
		}
	}
}

func TestServerConfigLoadsCertificatePathsAndTLSMode(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.yaml")
			yaml := "server:\n  tls_enabled: " + enabled + "\n  cert_file: cert/server.crt\n  key_file: cert/server.key\n"
			if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadServerConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Server.CertFile != "cert/server.crt" || cfg.Server.KeyFile != "cert/server.key" || cfg.IsTLSEnabled() != (enabled == "true") {
				t.Fatalf("certificate configuration changed: %+v", cfg.Server)
			}
		})
	}
}

func TestServerConfigRejectsAdditionalYAMLDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("server: {}\n---\nserver:\n  tls_enabled: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServerConfig(path); err == nil {
		t.Fatal("a second ignored server configuration was accepted")
	}
}

func TestClonedAgentConfigDoesNotShareTLSOption(t *testing.T) {
	cfg := &AgentConfigFile{}
	cfg.Server.TLSEnabled = BoolPtr(true)
	cloned := CloneAgentConfig(cfg)
	*cloned.Server.TLSEnabled = false
	if !cfg.IsServerTLSEnabled() {
		t.Fatal("mutating a returned config changed the original TLS setting")
	}
}

func TestClonedAgentConfigDoesNotShareRDPOption(t *testing.T) {
	cfg := &AgentConfigFile{}
	cfg.RDP.Enabled = BoolPtr(true)
	cloned := CloneAgentConfig(cfg)
	*cloned.RDP.Enabled = false
	if cfg.RDP.Enabled == nil || !*cfg.RDP.Enabled {
		t.Fatal("mutating a returned config changed the original RDP setting")
	}
}
