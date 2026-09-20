//go:build windows

package gui

import (
	"strings"
	"testing"
)

func TestConnectionsDocumentRemainsReusableByWails(t *testing.T) {
	dark, err := renderConnectionsHTML(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data-theme="dark"`,
		`connections.js`,
		`id="connections"`,
	} {
		if !strings.Contains(dark, want) {
			t.Fatalf("connections document missing %q", want)
		}
	}

	light, err := renderConnectionsHTML(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(light, `data-theme="dark"`) || !strings.Contains(light, `data-theme="light"`) {
		t.Fatal("light connection document did not switch theme")
	}
}

func TestWailsBridgeExposesConnectionBinding(t *testing.T) {
	data, err := assets.ReadFile("assets/wails-bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, want := range []string{
		`window.goGetConnections`,
		`invoke('GetConnections')`,
		`gui.WailsService.`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("Wails bridge missing %q", want)
		}
	}
}
