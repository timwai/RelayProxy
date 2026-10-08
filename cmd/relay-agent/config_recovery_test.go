package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeConfigFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSelectDefaultConfigPathRestoresLegacyConfigWithoutMovingIdentity(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "profile", ".relayproxy", agentConfigName)
	previous := filepath.Join(root, "old install", "configs", agentConfigName)
	previousYAML := "server:\n  address: previous.relay.test\ndevice:\n  identity_id: abcdefgh12345678\n  name: OriginalDevice\n"
	writeConfigFixture(t, previous, previousYAML)
	identity := filepath.Join(filepath.Dir(previous), "device-identity.json")
	writeConfigFixture(t, identity, "{\"installation_id\":\"original\"}")

	if chosen := selectDefaultConfigPath(current, []string{previous}); chosen != previous {
		t.Fatalf("legacy config ignored: got %q want %q", chosen, previous)
	}
	loaded, err := loadOrCreateAgentConfig(previous)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Server.Address != "previous.relay.test" || loaded.Device.Name != "OriginalDevice" {
		t.Fatalf("legacy settings lost: %+v", loaded)
	}
	saved, err := os.ReadFile(previous)
	if err != nil || !bytes.Equal(saved, []byte(previousYAML)) {
		t.Fatalf("original config was modified: %v", err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("profile config unexpectedly generated: %v", err)
	}
	if data, err := os.ReadFile(identity); err != nil || string(data) != "{\"installation_id\":\"original\"}" {
		t.Fatalf("original identity was modified: %q / %v", data, err)
	}
}

func TestSelectDefaultConfigPathReplacesOnlyUntouchedBootstrap(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".relayproxy", agentConfigName)
	previous := filepath.Join(root, "configs", agentConfigName)
	bootstrap := "server:\n    address: 127.0.0.1\n"
	writeConfigFixture(t, current, bootstrap)
	writeConfigFixture(t, previous, "server:\n  address: configured.example.test\n")
	if chosen := selectDefaultConfigPath(current, []string{previous}); chosen != previous {
		t.Fatalf("bootstrap masked previous config: %q", chosen)
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != bootstrap {
		t.Fatalf("bootstrap modified during discovery: %q / %v", data, err)
	}
	// A user-edited profile config has priority over any legacy file.
	writeConfigFixture(t, current, "server:\n  address: new.example.test\n")
	if chosen := selectDefaultConfigPath(current, []string{previous}); chosen != current {
		t.Fatalf("user profile config should win: %q", chosen)
	}
}

func TestSelectDefaultConfigPathNeverOverwritesInvalidSettings(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".relayproxy", agentConfigName)
	previous := filepath.Join(root, "configs", agentConfigName)
	invalid := "device:\n  token: old-secret\n"
	writeConfigFixture(t, previous, invalid)
	if chosen := selectDefaultConfigPath(current, []string{previous}); chosen != previous {
		t.Fatalf("invalid legacy config must be surfaced for diagnosis: %q", chosen)
	}
	if _, err := loadOrCreateAgentConfig(previous); err == nil {
		t.Fatal("invalid legacy config was silently replaced with defaults")
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("a default config was created despite invalid previous config: %v", err)
	}
	writeConfigFixture(t, current, invalid)
	if chosen := selectDefaultConfigPath(current, []string{previous}); chosen != current {
		t.Fatalf("invalid profile config must not be discarded: %q", chosen)
	}
}

func TestSelectDefaultConfigPathSkipsPackagedExample(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".relayproxy", agentConfigName)
	example := filepath.Join(root, "configs", agentConfigName)
	writeConfigFixture(t, example, "server:\n  address: relay.example.com\ndevice:\n  identity_id: a1b2c3d4e5f6g7h8\n  name: My-PC\n")
	if chosen := selectDefaultConfigPath(current, []string{example}); chosen != current {
		t.Fatalf("packaged sample selected as device config: %q", chosen)
	}
	if _, err := loadOrCreateAgentConfig(current); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(current); err != nil || !isBootstrapConfig(data) {
		t.Fatalf("first launch did not create normal bootstrap: %q / %v", data, err)
	}
}

func TestSelectDefaultConfigPathUsesOnlyLegacyBootstrapWhenNoProfile(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".relayproxy", agentConfigName)
	old := filepath.Join(root, "configs", agentConfigName)
	writeConfigFixture(t, old, "server:\n    address: 127.0.0.1\n")
	if chosen := selectDefaultConfigPath(current, []string{old, old}); chosen != old {
		t.Fatalf("old bootstrap should remain beside existing identity: %q", chosen)
	}
}
