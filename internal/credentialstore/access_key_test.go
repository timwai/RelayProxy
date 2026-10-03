package credentialstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccessKeyRoundTripAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), accessKeyFileName)
	const key = "rpk_0123456789abcdefghijklmnopqrstuvwxyz"

	if err := SaveAccessKey(path, key); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("credential permissions are too broad: %o", info.Mode().Perm())
	}

	got, err := LoadAccessKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("LoadAccessKey()=%q want %q", got, key)
	}

	if err := ClearAccessKey(path); err != nil {
		t.Fatal(err)
	}
	got, err = LoadAccessKey(path)
	if err != nil || got != "" {
		t.Fatalf("credential remained after clear: value=%q err=%v", got, err)
	}
}

func TestAccessKeyRejectsInvalidFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), accessKeyFileName)
	if err := SaveAccessKey(path, "not-an-access-key"); err == nil {
		t.Fatal("invalid access key was accepted")
	}
}

func TestPathForConfigKeepsCredentialOutsideYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "relay-agent.yaml")
	credentialPath := PathForConfig(configPath)
	if credentialPath == configPath || strings.HasSuffix(credentialPath, ".yaml") {
		t.Fatalf("credential path aliases normal config: %q", credentialPath)
	}
	if filepath.Dir(credentialPath) != filepath.Dir(configPath) {
		t.Fatalf("credential moved outside the per-user config directory: %q", credentialPath)
	}
}
