//go:build darwin

package gui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagementURLPrefersEffectiveRuntimeAddress(t *testing.T) {
	got := managementURL(nil, Options{WebURL: " http://127.0.0.1:18765 "})
	if got != "http://127.0.0.1:18765/" {
		t.Fatalf("managementURL() = %q", got)
	}
}

func TestManagementURLForLogRemovesToken(t *testing.T) {
	got := managementURLForLog("http://127.0.0.1:18765/?token=secret&view=status")
	if got != "http://127.0.0.1:18765/?view=status" {
		t.Fatalf("managementURLForLog() = %q", got)
	}
}

func TestMacOSDesktopStaysInMenuBarAfterWindowClose(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to resolve test source path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "macos", "RelayProxyDesktop", "AppDelegate.swift")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		"applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }",
		"NSStatusBar.system.statusItem",
		"func windowShouldClose(_ sender: NSWindow) -> Bool",
		"sender.orderOut(nil)",
		"func applicationShouldHandleReopen",
		"退出 RelayProxy",
		"point.3.connected.trianglepath.dotted",
		"Agent 正常运行",
		"打开控制台",
		"刷新管理界面",
		"复制管理地址",
		"menu.autoenablesItems = false",
		"WKScriptMessageHandler",
		"WKUIDelegate",
		"webView.uiDelegate = self",
		"runJavaScriptConfirmPanelWithMessage",
		"runJavaScriptAlertPanelWithMessage",
		"relayproxyLifecycle",
		"relayproxyRDP",
		"com.microsoft.rdc.macos",
		"NSWorkspace.OpenConfiguration",
		"withApplicationAt: applicationURL",
		"rdp://full%20address=s:",
		"func userContentController",
		"message.frameInfo.isMainFrame",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("macOS desktop lifecycle is missing %q", required)
		}
	}
	if strings.Contains(source, "applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }") {
		t.Fatal("closing the last macOS window still terminates RelayProxy")
	}
}

func TestEmbeddedQuitNotifiesMacOSDesktopLifecycle(t *testing.T) {
	data, err := assets.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`id="quit-button"`,
		"async function doQuit()",
		"relayproxyLifecycle",
		"lifecycle.postMessage('quit')",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("embedded quit flow is missing %q", required)
		}
	}
}
