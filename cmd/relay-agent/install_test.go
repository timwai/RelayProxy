package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/internal/config"
)

func TestInstallWizardValidatesAndNormalizesInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "配置 with spaces", agentConfigName)
	input := strings.NewReader(strings.Join([]string{
		"https://relay.example.test:443/path", "relay.example.test",
		"0", "65536", "not-a-port", "8443",
		"9443",
		"maybe", "N",
		"", "invalid", "abcdefghijklmnop", "A1B2C3D4E5F6G7H8",
		"测试设备",
	}, "\r\n") + "\r\n")
	var output bytes.Buffer
	cfg, err := configureInstall(path, input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "relay.example.test" || cfg.Server.TCPPort != 8443 || cfg.Server.QUICPort != 9443 {
		t.Fatalf("server settings did not match wizard input: %+v", cfg.Server)
	}
	if cfg.IsServerTLSEnabled() || cfg.Transport.Mode != "tcp_only" {
		t.Fatalf("plaintext setup did not select TCP: %+v", cfg.Transport)
	}
	if cfg.Device.IdentityID != "a1b2c3d4e5f6g7h8" || cfg.Device.Name != "测试设备" {
		t.Fatalf("device settings did not match wizard input: %+v", cfg.Device)
	}
	if strings.Count(output.String(), "输入无效") != 7 || !strings.Contains(output.String(), "此项不能为空") {
		t.Fatalf("invalid inputs were not retried: %s", output.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("wizard wrote to disk before all settings were collected: %v", err)
	}
}

func TestInstallWizardPreservesExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentConfigName)
	contents := "server:\n  address: existing.example.test\n  tcp_port: 8443\n  quic_port: 9443\ndevice:\n  identity_id: a1b2c3d4e5f6g7h8\n  name: Existing device\nproxy:\n  socks5:\n    enabled: false\n    port: 1085\nweb:\n  enabled: false\n  port: 9095\np2p:\n  mode: relay_only\n"
	writeConfigFixture(t, path, contents)
	var output bytes.Buffer
	cfg, err := configureInstall(path, strings.NewReader("\n\n\n\n\n\n"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Address != "existing.example.test" || cfg.Server.TCPPort != 8443 || cfg.Server.QUICPort != 9443 || !cfg.IsServerTLSEnabled() {
		t.Fatalf("existing server defaults were not preserved: %+v", cfg.Server)
	}
	if cfg.Device.IdentityID != "a1b2c3d4e5f6g7h8" || cfg.Device.Name != "Existing device" {
		t.Fatalf("existing device defaults were not preserved: %+v", cfg.Device)
	}
	if *cfg.Proxy.SOCKS5.Enabled || cfg.Proxy.SOCKS5.Port != 1085 || cfg.IsWebEnabled() || cfg.Web.Port != 9095 || cfg.P2P.Mode != "relay_only" {
		t.Fatal("wizard reset unrelated settings")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != contents {
		t.Fatalf("wizard modified existing file before saving: %q / %v", data, err)
	}
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadAgentConfig(path)
	if err != nil || *loaded.Proxy.SOCKS5.Enabled || loaded.Web.Port != 9095 || loaded.Device.Name != "Existing device" {
		t.Fatalf("wizard settings did not survive save/load: %+v / %v", loaded, err)
	}
}

func TestInstallWizardRejectsIncompleteInputWithoutWriting(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new config", true: "existing config"}[existing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "custom", agentConfigName)
			contents := "server:\n  address: existing.example.test\n"
			if existing {
				writeConfigFixture(t, path, contents)
			}
			_, err := configureInstall(path, strings.NewReader("relay.example.test\n443\n"), io.Discard)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("expected incomplete input error, got %v", err)
			}
			data, err := os.ReadFile(path)
			if existing {
				if err != nil || string(data) != contents {
					t.Fatalf("incomplete setup changed existing configuration: %q / %v", data, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("incomplete setup created configuration: %v", err)
			}
		})
	}
}

func TestInstallWizardRejectsInvalidExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentConfigName)
	contents := "server: [invalid YAML"
	writeConfigFixture(t, path, contents)
	if _, err := configureInstall(path, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("invalid existing config was accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != contents {
		t.Fatalf("invalid existing configuration was overwritten: %q / %v", data, err)
	}
}

func TestInstallCommandHelpAndInvalidFlags(t *testing.T) {
	var output bytes.Buffer
	if err := runInstall([]string{"--help"}, strings.NewReader(""), &output); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("install --help: %v", err)
	}
	if !strings.Contains(output.String(), "--config-dir") || !strings.Contains(output.String(), "后台") {
		t.Fatalf("install help is missing usage: %s", output.String())
	}
	for _, args := range [][]string{
		{"--config-dir="},
		{"--config=agent.yaml", "--config-dir=custom"},
		{"--gui"},
		{"extra"},
	} {
		if err := runInstall(args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatalf("invalid install arguments accepted: %q", args)
		}
	}
}
