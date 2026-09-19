// Package gui implements the RelayProxy desktop client: a native WebView2
// window (no browser, no console) with a system tray icon, wired to the agent
// core through the bridge package.
//
// The UI is plain HTML/CSS/JavaScript rendered from embedded assets, matching
// the design document's "HTML + native JavaScript + JS Bridge" constraint. On
// non-Windows platforms the desktop window is unavailable and Run reports it.
package gui

import (
	"encoding/json"
	"errors"
	"strings"

	"relayproxy/agent/app"
	"relayproxy/agent/bridge"
)

// ErrUnsupported is returned by Run on platforms without a desktop window.
var ErrUnsupported = errors.New("native desktop GUI is unavailable on this platform")

// ErrExternalUI means the platform opened the shared local management UI in an
// external system window/browser and the Agent should continue running.
var ErrExternalUI = errors.New("management UI opened externally")

// Version is the client version shown in the UI. Override at build time with
// -ldflags "-X relayproxy/agent/gui.Version=x.y.z".
var Version = "1.0.0"

// DefaultWindowTitle is intentionally stable across releases. The second
// instance uses it to locate and activate the first instance's native window.
const (
	DefaultWindowTitle  = "RelayProxy 代理客户端"
	DefaultWindowWidth  = 1100
	DefaultWindowHeight = 760
	MinimumWindowWidth  = 820
	MinimumWindowHeight = 560
)

// Options configures the desktop window.
type Options struct {
	// ConfigPath is the YAML file the agent was started with; it is shown in the
	// UI and passed to the autostart registry entry.
	ConfigPath string
	// StartMinimized boots straight into the system tray without showing the window.
	StartMinimized bool
	// MinimizeToTray makes the window's close button hide to the tray.
	MinimizeToTray bool
	// Theme is "dark", "light", or "system".
	Theme string
	// Title is the window title.
	Title string
	// Width and Height size the window; zero selects sensible defaults.
	Width  int
	Height int
}

func (o Options) theme() string {
	switch strings.ToLower(strings.TrimSpace(o.Theme)) {
	case "light":
		return "light"
	case "system":
		return "system"
	default:
		return "dark"
	}
}

// ui is the platform-independent surface the tray and bindings drive.
type ui interface {
	// pushLog appends a log line to the log view.
	pushLog(line string)
	// applyTheme swaps the UI theme without persisting it.
	applyTheme(dark bool)
	// refreshSidebar re-reads status into the sidebar widgets.
	refreshSidebar()
	// setAutostartChecked mirrors the login-startup state into the settings toggle.
	setAutostartChecked(enabled bool)
	// show or hide the main window.
	showWindow(visible bool)
	// eval runs JavaScript in the page.
	eval(script string)
}

// jsCall renders a call to a global JS hook that may not exist yet (the page can
// still be loading), so every push is defensive.
func jsCall(fn string, payload any) string {
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return "window." + fn + " && window." + fn + "(" + string(data) + ")"
}

// jsonString marshals a value into a JS string literal.
func jsonString(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(data)
}

// logPump forwards runtime log lines into the UI.
//
// Lines are pushed immediately via the JS hook, and the UI also polls goGetLogs
// on an interval so it can recover anything emitted before the page finished
// loading.
type logPump struct {
	target ui
	mu     chan struct{} // 1-slot semaphore, keeps the pump from unbounded growth
}

func newLogPump(target ui) *logPump {
	return &logPump{target: target, mu: make(chan struct{}, 1)}
}

func (p *logPump) run(entry app.LogEntry) {
	select {
	case p.mu <- struct{}{}:
	default:
		// A dispatch is already queued; dropping a duplicate line beats blocking
		// the goroutine that produced the log.
		return
	}
	go func() {
		defer func() { <-p.mu }()
		if script := jsCall("onGoLog", entry); script != "" {
			p.target.eval(script)
		}
	}()
}

// attachLogTap wires the running agent's log buffer into the UI.
func attachLogTap(b *bridge.UIBridge, target ui) {
	pump := newLogPump(target)
	b.SetLogTap(pump.run)
}

// detachLogTap stops forwarding logs (called on shutdown).
func detachLogTap(b *bridge.UIBridge) {
	if b != nil {
		b.SetLogTap(nil)
	}
}
