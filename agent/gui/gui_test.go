package gui

import (
	"strings"
	"testing"

	"relayproxy/internal/protocol"
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
		"goSetRemoteDesktopResolution",
		"goGetRemoteDesktopDiagnostics",
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

func TestRemoteDesktopStatsExposeRealtimeCongestionSignals(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"stats.path || status.pathUdp || 'relay'",
		"'Queue ' + Number(stats.sendQueueDelayMs || 0).toFixed(1) + ' ms'",
		"'Dropped ' + Number(stats.droppedFrames || 0)",
		"'Capture ' + stats.captureMs.toFixed(1) + ' ms'",
		"'Encode ' + stats.encodeMs.toFixed(1) + ' ms'",
		"'Target FPS ' + Number(stats.targetFps)",
		"var captureLabel = stats.captureBackend",
		"captureLabel += '/' + stats.captureFormat",
		"'Capture ' + captureLabel",
		"'Encoder ' + stats.encoderBackend + (stats.encoderHardware ? ' HW' : ' SW')",
		"stats.decoderBackend || (desktopVideoDecoder ? 'webcodecs' : '')",
		"'Media: ' + mediaLabel",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop stats UI missing %q", want)
		}
	}
}

func TestRemoteDesktopDisplaySelectionUsesAdvertisedTargetDisplays(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"desktopDisplaySelections: {}",
		"function remoteDesktopDisplaySelection(targetID)",
		"displayId: remoteDesktopDisplaySelection(targetID)",
		"Array.isArray(caps.displays)",
		"allDisplays.textContent = '全部显示器'",
		"state.desktopDisplaySelections[target.deviceId] = displaySelect.value",
		"remoteDesktopConnectOptions(targetID)",
		"status.displayName || status.displayId",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop display selection UI missing %q", want)
		}
	}
}

func TestRemoteDesktopSceneSelectorFeedsAdaptivePolicy(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="desktop-opt-scene"`,
		`<option value="office">办公</option>`,
		`<option value="performance">性能</option>`,
		`<option value="gaming">游戏</option>`,
		`<option value="quality">画质</option>`,
		`var scene = $('desktop-opt-scene') ? $('desktop-opt-scene').value : 'auto';`,
		`scene: scene || 'auto'`,
		`游戏 / 性能优先保持协商帧率`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop scene selector missing %q", want)
		}
	}
}

func TestRemoteDesktopWebCodecsTracksMediaGeneration(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"desktopFrameGeneration = 0",
		"desktopVideoGeneration = 0",
		"desktopVideoNeedsKeyFrame = true",
		"ensureDesktopVideoDecoder(codec, generation)",
		"desktopVideoGeneration === generation",
		"ensureDesktopVideoDecoder(frame.codec || 'avc1.42E01F', frame.generation)",
		"frameGeneration === desktopFrameGeneration",
		"if (desktopVideoNeedsKeyFrame && !frame.keyFrame)",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop generation-aware WebCodecs path missing %q", want)
		}
	}
}

func TestRemoteDesktopRuntimeResolutionSwitcher(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="desktop-runtime-resolution-wrap"`,
		`id="desktop-runtime-resolution"`,
		`id="desktop-runtime-resolution-apply"`,
		`id="desktop-runtime-resolution-max"`,
		"function updateDesktopRuntimeResolutionControl(status)",
		"status.codec === 'h264'",
		"status.codec === 'h265'",
		"status.maxWidth || status.width || 0",
		"status.maxHeight || status.height || 0",
		"option.disabled = !!(maxWidth && maxHeight && (width > maxWidth || height > maxHeight))",
		"async function setRemoteDesktopResolution()",
		"call('goSetRemoteDesktopResolution', width, height)",
		"value === 'max'",
		"'最高 ' + maxWidth + '×' + maxHeight",
		"等待新媒体 Generation",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop runtime resolution UI missing %q", want)
		}
	}
}

func TestRemoteDesktopHiddenHEVCUsesNativeViewer(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		"mime.indexOf('video/h265') === 0",
		"$('desktop-viewer-image').removeAttribute('src')",
		"H.265 · 请使用原生查看器 · QUIC Datagram · 键鼠控制",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("hidden HEVC native-viewer guard missing %q", want)
		}
	}
}

func TestRemoteDesktopDiagnosticsExport(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="desktop-diagnostics-export-btn"`,
		"async function exportRemoteDesktopDiagnostics()",
		"call('goGetRemoteDesktopDiagnostics')",
		"desktopDiagnosticsFilename(report)",
		"new Blob([payload], { type:'application/json;charset=utf-8' })",
		"link.download = desktopDiagnosticsFilename(report)",
		"URL.createObjectURL(blob)",
		"URL.revokeObjectURL(url)",
		"最近约 10 分钟",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop diagnostics export missing %q", want)
		}
	}
}

func TestRemoteDesktopCaptureBackendSelector(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="desktop-opt-capture"`,
		"function remoteDesktopCaptureBackends(target)",
		"Array.isArray(caps.captures)",
		"capture && capture.backend",
		"function remoteDesktopCaptureBackendSupported(targetID, backend)",
		"if (!captures.length) return backend === 'dxgi' || backend === 'gdi';",
		"function syncRemoteDesktopCaptureOptions()",
		"['wgc', 'WGC']",
		"['dxgi', 'DXGI']",
		"['gdi', 'GDI']",
		"syncRemoteDesktopCaptureOptions();",
		"var captureBackend = $('desktop-opt-capture') ? $('desktop-opt-capture').value : 'auto';",
		"captureBackend: captureBackend || 'auto'",
		"var relayRequired = options.backend === 'relay'",
		"!remoteDesktopCaptureBackendSupported(targetID, options.captureBackend)",
		"目标未提供 ' + options.captureBackend.toUpperCase() + ' 采集能力",
		"WGC 仅在目标 Windows 运行时确认支持时出现",
		"采集“自动”按 DXGI → WGC → GDI 顺序选择",
		"显式 WGC/DXGI/GDI 用于实机 A/B 验证且不会静默切换到另一后端",
		"强制 WGC/DXGI 时请先选择具体显示器",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop capture backend selector missing %q", want)
		}
	}
	if strings.Contains(page, `<option value="wgc">WGC</option>`) {
		t.Fatal("WGC must not be a static option; it must come from target capabilities")
	}
}

func TestRemoteDesktopHEVCValidationOverrideIsOptIn(t *testing.T) {
	base := protocol.RemoteDesktopConnectOptions{
		Backend: protocol.DesktopBackendAuto,
		Codec:   "h264",
	}
	for _, value := range []string{"", "0", "false", "off", "no"} {
		got := applyRemoteDesktopHEVCValidationOptions(base, value)
		if got.Backend != base.Backend || got.Codec != base.Codec {
			t.Fatalf("disabled validation env %q changed options: %+v", value, got)
		}
	}
	for _, value := range []string{"1", "true", "TRUE", " yes ", "on"} {
		got := applyRemoteDesktopHEVCValidationOptions(base, value)
		if got.Backend != protocol.DesktopBackendRelay || got.Codec != protocol.DesktopCodecH265Validation {
			t.Fatalf("enabled validation env %q produced %+v", value, got)
		}
	}
}

func TestRemoteDesktopLiveAudioStats(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`id="desktop-viewer-audio-stats"`,
		"async function refreshRemoteDesktopAudioStats()",
		"call('goGetRemoteDesktopAudioDiagnostics')",
		"audio.concealmentFrames",
		"audio.gapSkippedFrames",
		"audio.queueDroppedFrames",
		"refreshRemoteDesktopAudioStats();",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop live audio stats missing %q", want)
		}
	}
}


func TestRemoteDesktopFollowViewport(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{
		`<option value="auto">跟随窗口（自动）</option>`,
		`<option value="viewport">跟随窗口</option>`,
		"desktopFollowViewport: false",
		"function remoteDesktopViewportTarget(status)",
		"window.devicePixelRatio",
		"function remoteDesktopViewportMayGrow(status)",
		"async function applyRemoteDesktopViewportResolution()",
		"function scheduleRemoteDesktopViewportResolution()",
		"new ResizeObserver(function ()",
		"state.desktopFollowViewport = !!followViewport;",
		"state.desktopFollowViewport = false;",
		"scheduleRemoteDesktopViewportResolution();",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("remote desktop follow viewport missing %q", want)
		}
	}
	if strings.Contains(page, `</div>\n              <div id="desktop-viewer-audio-stats"`) {
		t.Fatal("remote desktop viewer contains a literal \\n before audio diagnostics")
	}
}
