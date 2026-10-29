package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/agent/routing"
	"relayproxy/internal/config"
)

func environmentValues(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestStartupEnvironmentDirectoryAndCLIPaths(t *testing.T) {
	root := t.TempDir()
	getenv := environmentValues(map[string]string{"RELAY_AGENT_CONFIG_DIR": filepath.Join(root, "env config")})
	for _, tc := range []struct {
		file, dir       string
		fileSet, dirSet bool
		want            string
	}{
		{want: filepath.Join(root, "env config", agentConfigName)},
		{file: filepath.Join(root, "explicit.yaml"), fileSet: true, want: filepath.Join(root, "explicit.yaml")},
		{dir: filepath.Join(root, "cli"), dirSet: true, want: filepath.Join(root, "cli", agentConfigName)},
	} {
		got, err := resolveStartupConfigPath(tc.file, tc.dir, tc.fileSet, tc.dirSet, getenv)
		if err != nil || got != tc.want {
			t.Fatalf("config path = %q, %v; want %q", got, err, tc.want)
		}
	}
	if _, err := resolveStartupConfigPath("", "", true, false, getenv); err == nil {
		t.Fatal("environment hid an invalid explicit CLI path")
	}
}

func TestEnvironmentOverridesPersistAndPreserveIdentityAndPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), agentConfigName)
	cfg := defaultConfigFile()
	cfg.Device.IdentityID = "abcdef1234567890"
	cfg.Network.Mode = "divert"
	cfg.Network.ExcludeProcesses = []string{"sshd"}
	cfg.Routing.Rules = []routing.Rule{{Name: "keep", Enabled: true, Targets: []string{"example.test"}, Action: routing.ActionDirect}}
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(filepath.Dir(path), "device-identity.json")
	if err := os.WriteFile(identityPath, []byte("original installation identity"), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"RELAY_AGENT_SERVER": "relay.example.test", "RELAY_AGENT_SERVER_TCP_PORT": "2443", "RELAY_AGENT_SERVER_QUIC_PORT": "2444",
		"RELAY_AGENT_DEVICE_NAME": "NAS #1: $test", "RELAY_AGENT_IDENTITY_ID": "", "RELAY_AGENT_TRANSPORT_MODE": "tcp_only",
		"RELAY_AGENT_SOCKS5_ENABLED": "false", "RELAY_AGENT_HTTP_ENABLED": "false", "RELAY_AGENT_WEB_ENABLED": "true",
		"RELAY_AGENT_WEB_LISTEN": "0.0.0.0", "RELAY_AGENT_WEB_PORT": "19090", "RELAY_AGENT_WEB_TOKEN": "test-token-'$#:-with-32-or-more-bytes",
		"RELAY_AGENT_TRANSPARENT_PROXY": "false",
	}
	if _, err := loadStartupAgentConfig(path, environmentValues(values)); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Server.Address != values["RELAY_AGENT_SERVER"] || got.Server.TCPPort != 2443 || got.Server.QUICPort != 2444 || got.Transport.Mode != "tcp_only" {
		t.Fatal("server environment overrides were not saved")
	}
	if got.Device.IdentityID != "abcdef1234567890" || got.Device.Name != values["RELAY_AGENT_DEVICE_NAME"] || len(got.Routing.Rules) != 1 || got.Routing.Rules[0].Name != "keep" {
		t.Fatal("environment update erased identity or routing policy")
	}
	if got.Network.Mode != "" || len(got.Network.ExcludeProcesses) != 1 || *got.Proxy.SOCKS5.Enabled || *got.Proxy.HTTP.Enabled || !got.IsWebEnabled() {
		t.Fatal("explicit false values or unrelated exclusions were lost")
	}
	if got.Web.Port != 19090 || got.Web.Listen != "0.0.0.0" || got.Web.Token != values["RELAY_AGENT_WEB_TOKEN"] {
		t.Fatal("Web environment values were not safely serialized")
	}
	if data, err := os.ReadFile(identityPath); err != nil || string(data) != "original installation identity" {
		t.Fatal("environment update modified device identity file")
	}
	before, _ := os.ReadFile(path)
	if _, err := loadStartupAgentConfig(path, environmentValues(nil)); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("empty environment rewrote existing settings")
	}
}

func TestInvalidEnvironmentDoesNotOverwriteConfig(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"RELAY_AGENT_WEB_PORT", "0"}, {"RELAY_AGENT_SERVER_TCP_PORT", "65536"},
		{"RELAY_AGENT_HTTP_PORT", "invalid"}, {"RELAY_AGENT_TRANSPARENT_PROXY", "maybe"},
		{"RELAY_AGENT_WEB_TOKEN", "short-test-secret"}, {"RELAY_AGENT_IDENTITY_ID", "invalid"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), agentConfigName)
			writeConfigFixture(t, path, "server:\n  address: previous.example.test\n")
			before, _ := os.ReadFile(path)
			_, err := loadStartupAgentConfig(path, environmentValues(map[string]string{tc.name: tc.value, "RELAY_AGENT_DEVICE_NAME": "should not persist"}))
			if err == nil {
				t.Fatal("invalid environment accepted")
			}
			if tc.name == "RELAY_AGENT_WEB_TOKEN" && strings.Contains(err.Error(), tc.value) {
				t.Fatal("validation error disclosed token")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid environment overwrote existing configuration")
			}
		})
	}
}

func TestEnvironmentFirstLaunchAndTransparentProxyToggle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new config", agentConfigName)
	getenv := environmentValues(map[string]string{"RELAY_AGENT_SERVER": "127.0.0.1", "RELAY_AGENT_TRANSPARENT_PROXY": "true"})
	cfg, err := loadStartupAgentConfig(path, getenv)
	if err != nil || cfg.Network.Mode != "divert" {
		t.Fatalf("first-run transparent environment config failed: %v", err)
	}
	stored, err := config.LoadAgentConfig(path)
	if err != nil || stored.Network.Mode != "divert" {
		t.Fatal("first-run settings were not saved")
	}
}
