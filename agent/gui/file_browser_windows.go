//go:build windows

package gui

import (
	"net/url"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// A dedicated resizable WebView shares the Agent's existing authenticated
// Wails services; no extra public listener or filesystem access is introduced.
func (a *appWindow) openFileBrowser(deviceID, shareID, relativePath string) {
	if a == nil || a.app == nil {
		return
	}
	a.fileBrowserMu.Lock()
	if a.fileBrowser != nil {
		window := a.fileBrowser
		a.fileBrowserMu.Unlock()
		window.Show().Focus()
		if deviceID != "" && shareID != "" {
			if relativePath == "" {
				relativePath = "."
			}
			window.ExecJS("window.relayOpenFileShare && window.relayOpenFileShare(" +
				jsonString(deviceID) + "," + jsonString(shareID) + "," + jsonString(relativePath) + ")")
		}
		return
	}

	query := url.Values{"page": {"files"}, "filesWindow": {"1"}}
	if deviceID != "" && shareID != "" {
		if relativePath == "" {
			relativePath = "."
		}
		query.Set("fileDevice", deviceID)
		query.Set("fileShare", shareID)
		query.Set("filePath", relativePath)
	}
	background := application.BackgroundTypeSolid
	winopts := application.WindowsWindow{}
	if supportsMicaBackdrop() {
		background = application.BackgroundTypeTranslucent
		winopts.BackdropType = application.Mica
	}
	window := a.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:                       "file-browser",
		Title:                      "RelayProxy · 文件浏览器",
		Width:                      1540,
		Height:                     960,
		MinWidth:                   790,
		MinHeight:                  520,
		InitialPosition:            application.WindowCentered,
		BackgroundType:             background,
		Windows:                    winopts,
		DefaultContextMenuDisabled: true,
		DevToolsEnabled:            false,
	})
	a.fileBrowser = window
	a.fileBrowserMu.Unlock()

	window.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		a.fileBrowserMu.Lock()
		if a.fileBrowser == window {
			a.fileBrowser = nil
		}
		a.fileBrowserMu.Unlock()
	})
	// Load the embedded route explicitly after creating the WebView. This
	// matches Wails v3 multi-window asset loading and avoids blank child views.
	window.SetURL("/?" + query.Encode())
	window.Center()
	window.Show().Focus()
}

func (a *appWindow) closeFileBrowser() {
	if a == nil {
		return
	}
	a.fileBrowserMu.Lock()
	window := a.fileBrowser
	a.fileBrowser = nil
	a.fileBrowserMu.Unlock()
	if window != nil {
		window.Close()
	}
}
