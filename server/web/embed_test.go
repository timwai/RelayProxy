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
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("server console missing %q", want)
		}
	}
}
