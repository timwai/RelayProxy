package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"relayproxy/internal/config"
)

func TestExplicitConfigPathRemainsIndependent(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "启动目录 with spaces")
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)
	path, err := resolveConfigPath(filepath.Join("custom", "agent.yaml"), true)
	if err != nil || path != filepath.Join(workDir, "custom", "agent.yaml") {
		t.Fatalf("explicit path resolved to %q, err=%v", path, err)
	}
	if _, err := resolveConfigPath("", true); err == nil {
		t.Fatal("an explicitly empty --config silently selected another file")
	}
}

func TestStartupCreatesMinimalConfigAndReusesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user", ".relayproxy", agentConfigName)
	created, err := loadOrCreateAgentConfig(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("server:\n    address: 127.0.0.1\n")) {
		t.Fatalf("bootstrap config is not minimal:\n%s", data)
	}
	if created.Mode != "CLIENT" || created.Server.TCPPort != 443 || created.Server.QUICPort != 443 {
		t.Fatalf("in-memory defaults are incomplete: %+v", created)
	}
	before := append([]byte(nil), data...)
	loaded, err := loadOrCreateAgentConfig(path, []string{filepath.Join(t.TempDir(), "ignored-old.yaml")})
	if err != nil || loaded.Server.Address != "127.0.0.1" {
		t.Fatalf("existing config could not be reused: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("loading an existing file rewrote it")
	}
}

func TestStartupRejectsLegacyCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentConfigName)
	legacy := []byte("server:\n  address: relay.example.test\ndevice:\n  id: old\n  token: secret\n")
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateAgentConfig(path, nil); err == nil {
		t.Fatal("legacy credential configuration was accepted")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Equal(data, legacy) {
		t.Fatal("rejected legacy config was modified")
	}
}

func TestBootstrapConfigLoadsThroughPublicLoader(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentConfigName)
	if err := config.SaveBootstrapAgentConfig(path, "relay.example.test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadAgentConfig(path)
	if err != nil || cfg.Server.Address != "relay.example.test" || cfg.Mode != "CLIENT" {
		t.Fatalf("bootstrap config did not load: cfg=%+v err=%v", cfg, err)
	}
}
