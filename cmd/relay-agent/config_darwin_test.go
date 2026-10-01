//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinDefaultConfigUsesUserHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "用户 home")
	t.Setenv("HOME", home)

	path, err := resolveConfigPath("", false)
	if err != nil || path != filepath.Join(home, ".relayproxy", agentConfigName) {
		t.Fatalf("unexpected default path %q: %v", path, err)
	}
	if _, err := loadOrCreateAgentConfig(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("home config was not generated: %v", err)
	}
}
