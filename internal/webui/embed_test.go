package webui

import (
	"strings"
	"testing"
)

func TestSharedDesignSystemSupportsThemesAndDesktopPlatforms(t *testing.T) {
	css, err := ReadAsset("base.css")
	if err != nil {
		t.Fatal(err)
	}
	theme, err := ReadAsset("theme.js")
	if err != nil {
		t.Fatal(err)
	}
	cssText := string(css)
	themeText := string(theme)
	for _, want := range []string{
		`html[data-platform="windows"]`,
		`html[data-platform="macos"]`,
		`--rp-control-height`,
		`.rp-tabs`,
		`.rp-theme-control`,
	} {
		if !strings.Contains(cssText, want) {
			t.Fatalf("shared CSS missing %q", want)
		}
	}
	for _, want := range []string{
		`platformName()`,
		`relayproxy-ui-theme`,
		`prefers-color-scheme: dark`,
		`data-rp-theme`,
	} {
		if !strings.Contains(themeText, want) {
			t.Fatalf("shared theme runtime missing %q", want)
		}
	}
}
