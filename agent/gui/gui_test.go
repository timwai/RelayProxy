package gui

import (
	"strings"
	"testing"
)

func TestDesktopWindowDimensionsRemainResponsive(t *testing.T) {
	if DefaultWindowWidth <= MinimumWindowWidth || DefaultWindowHeight <= MinimumWindowHeight {
		t.Fatalf("default window must be larger than minimum: default=%dx%d min=%dx%d",
			DefaultWindowWidth, DefaultWindowHeight, MinimumWindowWidth, MinimumWindowHeight)
	}
	if MinimumWindowWidth > 900 || MinimumWindowHeight > 600 {
		t.Fatalf("minimum window is too large for compact desktop layouts: %dx%d",
			MinimumWindowWidth, MinimumWindowHeight)
	}
}

func TestDefaultWindowTitleIsProductNameOnly(t *testing.T) {
	if DefaultWindowTitle != "RelayProxy" {
		t.Fatalf("default window title = %q, want RelayProxy", DefaultWindowTitle)
	}
}

func TestWailsBridgeCoversAgentFrontendBindings(t *testing.T) {
	data, err := assets.ReadFile("assets/wails-bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	if !strings.Contains(script, "relayproxy/agent/gui.WailsService.") {
		t.Fatal("Wails bridge must call the runtime binding with the full Go package path")
	}
	if strings.Contains(script, "'gui.WailsService.") {
		t.Fatal("Wails bridge still uses the invalid short package name")
	}
	for _, name := range []string{
		"goClearLogs",
		"goCopyClipboard",
		"goGetConfig",
		"goGetConnections",
		"goGetLogs",
		"goGetStatus",
		"goOpenConfigDir",
		"goOpenConnections",
		"goQuit",
		"goReloadConfig",
		"goRestart",
		"goSaveConfig",
		"goSelectExit",
		"goSetAutostart",
		"goSetTheme",
	} {
		if !strings.Contains(script, "window."+name) {
			t.Fatalf("Wails bridge missing %s", name)
		}
	}
}
