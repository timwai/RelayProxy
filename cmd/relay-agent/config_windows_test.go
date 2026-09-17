//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsDefaultConfigUsesUserProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "用户 profile")
	t.Setenv("USERPROFILE", profile)
	path, err := resolveConfigPath("", false)
	if err != nil || path != filepath.Join(profile, ".relayproxy", agentConfigName) {
		t.Fatalf("unexpected default path %q: %v", path, err)
	}
	if _, err := loadOrCreateAgentConfig(path, []string{filepath.Join(t.TempDir(), "old.yaml")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("profile config was not generated: %v", err)
	}
}
