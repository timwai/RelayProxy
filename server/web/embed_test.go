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
		`relayproxy-server-settings-tab`,
		`#settings/`,
		`role', 'tab'`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("server navigation missing %q", want)
		}
	}
}

func TestServerConsoleThemeUsesSharedTokens(t *testing.T) {
	style, err := EmbeddedFiles.ReadFile("css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	text := string(style)
	for _, want := range []string{
		`background:var(--paper)`,
		`var(--rp-surface-soft`,
		`color-mix(in srgb,var(--paper)`,
		`html[data-theme="dark"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("server theme CSS missing %q", want)
		}
	}
}

func TestServerConsoleLoadsSharedFoundationBeforeProductCSS(t *testing.T) {
	data, err := EmbeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	shared := strings.Index(page, `href="/ui/base.css"`)
	product := strings.Index(page, `href="/css/style.css"`)
	if shared < 0 || product < 0 || shared > product {
		t.Fatalf("shared CSS must load before product CSS: shared=%d product=%d", shared, product)
	}

	style, err := EmbeddedFiles.ReadFile("css/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(style), `margin-left:var(--rp-sidebar-width)`) {
		t.Fatal("server layout no longer follows shared sidebar width")
	}
}
