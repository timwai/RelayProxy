//go:build windows

package gui

import (
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func (a *appWindow) openConnections() {
	if a == nil || a.app == nil {
		return
	}

	a.monitorMu.Lock()
	if a.monitor != nil {
		window := a.monitor
		a.monitorMu.Unlock()
		window.Show().Focus()
		return
	}

	window := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "connections",
		Title:                      "RelayProxy · 实时连接",
		Width:                      1200,
		Height:                     720,
		MinWidth:                   850,
		MinHeight:                  420,
		URL:                        "/connections.html",
		InitialPosition:            application.WindowCentered,
		DefaultContextMenuDisabled: true,
		DevToolsEnabled:            false,
	})
	a.monitor = window
	a.monitorMu.Unlock()

	window.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.monitorMu.Lock()
		if a.monitor == window {
			a.monitor = nil
		}
		a.monitorMu.Unlock()
	})
	window.Center()
	window.Show().Focus()
}

func (a *appWindow) closeConnections() {
	if a == nil {
		return
	}
	a.monitorMu.Lock()
	window := a.monitor
	a.monitor = nil
	a.monitorMu.Unlock()
	if window != nil {
		window.Close()
	}
}

// renderConnectionsHTML is kept as a small testable helper for the embedded
// monitor document. The Wails asset server adds the runtime/bridge layer in
// buildWailsAssets.
func renderConnectionsHTML(dark bool) (string, error) {
	raw, err := assets.ReadFile("assets/connections.html")
	if err != nil {
		return "", err
	}
	html := string(raw)
	if !dark {
		html = strings.Replace(html, "data-theme=\"dark\"", "data-theme=\"light\"", 1)
	}
	if !strings.Contains(html, "connections.js") {
		return "", fmt.Errorf("连接监控页面缺少 connections.js")
	}
	return html, nil
}
