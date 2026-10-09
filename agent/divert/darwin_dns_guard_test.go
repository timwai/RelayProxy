//go:build darwin

package divert

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinPersistentDNSGuardMarker(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "ipc.token")
	marker := filepath.Join(dir, "dns-guard.enabled")
	if err := writeDarwinGuardMarker(tokenPath, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "enabled\n" {
		t.Fatalf("guard marker missing: %q %v", data, err)
	}
	stat, err := os.Stat(marker)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("guard marker permissions not secure: %v %v", stat, err)
	}
	if err := writeDarwinGuardMarker(tokenPath, true); err != nil {
		t.Fatalf("re-arm failed: %v", err)
	}
	if err := writeDarwinGuardMarker(tokenPath, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("disabling guard did not remove persistent marker: %v", err)
	}
}
