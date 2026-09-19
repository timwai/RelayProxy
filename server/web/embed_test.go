package web

import (
	"strings"
	"testing"
)

func TestConsoleUsesUnifiedPersonalNavigation(t *testing.T) {
	data, err := EmbeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`data-section="overview"`,
		`data-section="devices"`,
		`data-section="connections"`,
		`id="secondary-tabs"`,
		`data-rp-theme="system"`,
		`/ui/base.css`,
		`/ui/theme.js`,
		`data-settings-panel="admin"`,
		`data-settings-panel="tunnel"`,
		`data-settings-panel="rdp"`,
		`data-settings-panel="certificate"`,
		`data-settings-panel="acl"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("server console missing %q", want)
		}
	}
}


func TestServerConsoleKeepsLargeMenuAndSecondarySettingsTabs(t *testing.T) {
	script, err := EmbeddedFiles.ReadFile("js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, want := range []string{
		`settingsTab: 'admin'`,
		`settingsTab: 'tunnel'`,
		`settingsTab: 'rdp'`,
		`settingsTab: 'certificate'`,
		`settingsTab: 'acl'`,
		`applySettingsSubtab()`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("server navigation missing %q", want)
		}
	}
}
