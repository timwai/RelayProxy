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

func TestRoutingRulesUseReadOnlyListAndModalEditor(t *testing.T) {
	indexData, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	routingData, err := assets.ReadFile("assets/routing.js")
	if err != nil {
		t.Fatal(err)
	}
	index := string(indexData)
	script := string(routingData)

	for _, want := range []string{
		`id="routing-rule-modal"`,
		`id="routing-rule-name"`,
		`id="routing-rule-processes"`,
		`id="routing-rule-targets"`,
		`id="routing-rule-ports"`,
		`id="routing-rule-action"`,
		`id="routing-rule-save"`,
		`class="routing-rule-table"`,
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("routing editor UI missing %q", want)
		}
	}
	for _, want := range []string{
		"openRuleEditor(-1)",
		"openRuleEditor(index)",
		"saveRuleEditor()",
		"routing-edit-button",
		"routing-status",
		"routing-token-list",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("routing script missing modal/list behavior %q", want)
		}
	}

	start := strings.Index(script, "function createRuleRow")
	end := strings.Index(script, "function unconstrainedRule")
	if start < 0 || end <= start {
		t.Fatal("unable to locate routing rule row renderer")
	}
	rowRenderer := script[start:end]
	for _, forbidden := range []string{"<input", "<select", "<textarea", "oninput=", "onchange="} {
		if strings.Contains(rowRenderer, forbidden) {
			t.Fatalf("routing list must be read-only; row renderer contains %q", forbidden)
		}
	}
	if strings.Contains(script, `oninput="routingRules[`) {
		t.Fatal("routing script still contains legacy inline rule editing")
	}
}


func TestSettingsDoNotHideManualLaunch(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, forbidden := range []string{
		`id="cfg-start-min"`,
		"启动时直接进入托盘",
		"onToggleStartMin",
	} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("settings still expose manual start-minimized behavior %q", forbidden)
		}
	}
}
