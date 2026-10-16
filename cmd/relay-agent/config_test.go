package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/internal/config"
)

func TestExplicitConfigPathRemainsIndependent(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "启动目录 with spaces")
	if err := os.MkdirAll(workDir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)
	path, err := resolveConfigPath(filepath.Join("custom", "agent.yaml"), "", true, false)
	if err != nil || path != filepath.Join(workDir, "custom", "agent.yaml") {
		t.Fatalf("explicit path resolved to %q, err=%v", path, err)
	}
	if _, err := resolveConfigPath("", "", true, false); err == nil {
		t.Fatal("an explicitly empty --config silently selected another file")
	}
}

func TestExplicitConfigDirectory(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	for _, directory := range []string{
		".",
		filepath.Join("custom", "配置 with spaces"),
		filepath.Join(workDir, "absolute", "配置 with spaces"),
		"custom" + string(filepath.Separator),
	} {
		t.Run(directory, func(t *testing.T) {
			path, err := resolveConfigPath("", directory, false, true)
			want := filepath.Join(directory, agentConfigName)
			if !filepath.IsAbs(want) {
				want = filepath.Join(workDir, want)
			}
			if err != nil || path != want {
				t.Fatalf("directory resolved to %q, err=%v; want %q", path, err, want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("resolving configuration path unexpectedly created a file: %v", err)
			}
		})
	}
}

func TestConfigPathRejectsInvalidDirectoryFlags(t *testing.T) {
	for _, tc := range []struct {
		name      string
		file      string
		directory string
		explicit  bool
		wantError string
	}{
		{name: "empty directory", wantError: "--config-dir must specify a directory path"},
		{name: "blank directory", directory: " \t ", wantError: "--config-dir must specify a directory path"},
		{name: "file and directory", file: "agent.yaml", directory: "custom", explicit: true, wantError: "--config and --config-dir cannot be used together"},
		{name: "explicit empty file and directory", directory: "custom", explicit: true, wantError: "--config and --config-dir cannot be used together"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := resolveConfigPath(tc.file, tc.directory, tc.explicit, true)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || path != "" {
				t.Fatalf("resolveConfigPath() = %q, %v; want error %q", path, err, tc.wantError)
			}
		})
	}
}

func TestConfigDirectoryCreatesAndReusesConfiguration(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	// An explicitly selected directory must not fall back to a configured file
	// in the working directory when its own configuration does not exist yet.
	writeConfigFixture(t, filepath.Join(workDir, "configs", agentConfigName), "server:\n  address: fallback.example.test\n")
	path, err := resolveConfigPath("", filepath.Join("custom", "配置 with spaces"), false, true)
	if err != nil {
		t.Fatal(err)
	}
	created, err := loadOrCreateAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !isBootstrapConfig(data) || created.Server.Address != "127.0.0.1" {
		t.Fatalf("custom directory did not receive bootstrap config: %q / %v", data, err)
	}
	configured := "server:\n  address: custom.example.test\n"
	writeConfigFixture(t, path, configured)
	loaded, err := loadOrCreateAgentConfig(path)
	if err != nil || loaded.Server.Address != "custom.example.test" {
		t.Fatalf("custom directory config was not reused: cfg=%+v err=%v", loaded, err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != configured {
		t.Fatalf("custom directory config was rewritten: %q / %v", data, err)
	}
}

func TestStartupCreatesMinimalConfigAndReusesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user", ".relayproxy", agentConfigName)
	created, err := loadOrCreateAgentConfig(path)
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
	loaded, err := loadOrCreateAgentConfig(path)
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
	if _, err := loadOrCreateAgentConfig(path); err == nil {
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
