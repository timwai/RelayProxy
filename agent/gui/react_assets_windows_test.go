//go:build windows

package gui

import (
	"strings"
	"testing"
)

func TestWindowsWailsPrefersBuiltReactFrontend(t *testing.T) {
	if _, err := assets.ReadFile("react_dist/index.html"); err != nil {
		t.Skip("React dist has not been generated in this Go-only checkout")
	}

	mainHTML, connectionsHTML, files, err := buildWailsAssets(Options{Theme: "dark"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id="root"`,
		`/wails/runtime.js`,
		`/wails-bridge.js`,
		`data-theme-mode="dark"`,
		`data-theme="dark"`,
	} {
		if !strings.Contains(mainHTML, want) {
			t.Fatalf("React Wails page missing %q", want)
		}
	}
	if strings.Contains(mainHTML, `id="section-btn-network"`) {
		t.Fatal("Windows Wails page unexpectedly fell back to legacy management UI")
	}
	for _, name := range []string{"wails-bridge.js", "verification.html", "connections.js"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("Wails React asset map missing %s", name)
		}
	}
	if !strings.Contains(connectionsHTML, "connections.js") || !strings.Contains(connectionsHTML, "/wails/runtime.js") {
		t.Fatal("React asset switch broke the dedicated connection monitor")
	}

	foundBundle := false
	for name := range files {
		if strings.HasPrefix(name, "assets/") && strings.HasSuffix(name, ".js") {
			foundBundle = true
			break
		}
	}
	if !foundBundle {
		t.Fatal("Vite JavaScript bundle was not embedded")
	}
}
