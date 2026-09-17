package deviceidentity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateStableIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-identity.json")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.InstallationID != second.InstallationID || first.Fingerprint() != second.Fingerprint() {
		t.Fatal("identity changed after reload")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("identity permissions are not private: info=%v err=%v", info, err)
	}
}

func TestLoadOrCreateAcceptsPermissivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "device-identity.json")
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}

	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("permissive identity permissions must not block loading: %v", err)
	}
	if first.InstallationID != second.InstallationID || first.Fingerprint() != second.Fingerprint() {
		t.Fatal("identity changed after loading with permissive permissions")
	}
}
