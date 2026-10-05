package web

import (
	"strings"
	"testing"
)

func TestServerConsoleHidesAuthViewsUntilSessionCheck(t *testing.T) {
	data, err := EmbeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="login-view" class="login-view" hidden`,
		`id="app-view" class="app-shell rp-shell" hidden`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("server console auth view must start hidden: missing %q", want)
		}
	}
}

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
		`data-settings-panel="p2p"`,
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
		`settingsTab: 'p2p'`,
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

func TestServerP2PRendezvousSettingsAreEmbedded(t *testing.T) {
	page, err := EmbeddedFiles.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(page)
	for _, setting := range []string{"p2p.rendezvousListen", "p2p.rendezvousAdvertise", "p2p.portStart", "p2p.portEnd", "p2p.upnpEnabled"} {
		if !strings.Contains(text, `data-setting="`+setting+`"`) {
			t.Fatalf("server P2P setting %s is missing from the embedded form", setting)
		}
	}
}

func TestServerSettingsFormInitializesEverySettingGroup(t *testing.T) {
	script, err := EmbeddedFiles.ReadFile("js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if !strings.Contains(text, "rdpIngress: {}, p2p: {}") {
		t.Fatal("server settings form does not initialize the P2P settings group")
	}
}
