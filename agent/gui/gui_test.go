package gui

import (
	"strings"
	"testing"

	"relayproxy/agent/app"
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
		"goGetDiagnostics",
		"goRunSpeedTest",
		"goGetLogs",
		"goGetProxyExits",
		"goGetRDPTargets",
		"goGetStatus",
		"goOpenConfigDir",
		"goOpenConnections",
		"goQuit",
		"goReloadConfig",
		"goRestart",
		"goSaveConfig",
		"goSelectExit",
		"goConnectRDP",
		"goDisconnectRDP",
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

func TestDesktopPopupCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		message app.Message
		want    bool
	}{
		{
			name:    "legacy verification",
			message: app.Message{VerificationCode: "482931"},
			want:    true,
		},
		{
			name:    "legacy normal message",
			message: app.Message{Content: "hello"},
			want:    false,
		},
		{
			name:    "mixed-version popup without type",
			message: app.Message{Content: "hello", Popup: true},
			want:    true,
		},
		{
			name:    "explicit disabled verification",
			message: app.Message{VerificationCode: "482931", PopupType: "verification_code", Popup: false},
			want:    false,
		},
		{
			name:    "normal message popup",
			message: app.Message{Content: "hello", PopupType: "message", Popup: true},
			want:    true,
		},
		{
			name:    "typed normal message popup",
			message: app.Message{Content: "hello", MessageType: "message", Popup: true},
			want:    true,
		},
		{
			name:    "typed normal message explicitly disabled",
			message: app.Message{Content: "hello", MessageType: "message", Popup: false},
			want:    false,
		},
		{
			name:    "important popup",
			message: app.Message{Content: "urgent", PopupType: "important", Popup: true},
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPopupMessage(tc.message); got != tc.want {
				t.Fatalf("shouldPopupMessage(%+v) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}

func TestDesktopPopupAssetSupportsAllMessageTypes(t *testing.T) {
	data, err := assets.ReadFile("assets/verification.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"window.enqueueMessage",
		"window.__relayPendingMessages",
		"window.relayMessagePopupReady",
		"message.messageType",
		"verification_code",
		"important",
		"MESSAGE",
		"复制验证码",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("desktop popup asset missing %q", want)
		}
	}
}

func TestProxyExitInventoryLoadingStateCannotBeBlockedByRoutingUI(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	start := strings.Index(page, "function syncProxyExitSelectors()")
	if start < 0 {
		t.Fatal("unable to locate proxy exit selector synchronization")
	}
	rest := page[start:]
	end := strings.Index(rest, "async function refreshProxyExits")
	if end < 0 {
		t.Fatal("unable to locate proxy exit refresh function")
	}
	syncBlock := rest[:end]
	hint := strings.Index(syncBlock, "renderProxyExitInventoryHint();")
	routing := strings.Index(syncBlock, "window.syncRoutingExitOptions();")
	if hint < 0 || routing < 0 || hint > routing {
		t.Fatal("core exit inventory hint must update before optional routing synchronization")
	}
	for _, want := range []string{
		"try {",
		"console.warn('failed to sync routing exit options'",
		"state.proxyExitsLoaded = true;",
		"state.proxyExitsError = '';",
		"读取服务端授权出口失败，正在自动重试",
		"window.goGetProxyExits()",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("proxy exit inventory recovery logic missing %q", want)
		}
	}
}

func TestDesktopStatusDrivesProxyExitInventory(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	start := strings.Index(page, "function renderStatus(st)")
	if start < 0 {
		t.Fatal("renderStatus not found")
	}
	block := page[start:]
	end := strings.Index(block, "var up = !!st.connected")
	if end < 0 {
		t.Fatal("renderStatus status prelude not found")
	}
	block = block[:end]
	for _, want := range []string{
		"Array.isArray(st.proxyExits)",
		"state.proxyExits = st.proxyExits;",
		"state.proxyExitsLoaded = true;",
		"syncProxyExitSelectors();",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("status-driven exit inventory missing %q", want)
		}
	}
	if !strings.Contains(page, "if (!state.proxyExitsLoaded) refreshProxyExits(false);") {
		t.Fatal("legacy proxy exit endpoint must be fallback-only")
	}
}
