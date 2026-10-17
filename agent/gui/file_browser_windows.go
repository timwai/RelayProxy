//go:build windows

package gui

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// A dedicated resizable WebView shares the Agent's existing authenticated
// Wails services; no extra public listener or filesystem access is introduced.
func (a *appWindow) openFileBrowser() {
	if a == nil || a.app == nil {
		return
	}
	a.fileBrowserMu.Lock()
	if a.fileBrowser != nil {
		window := a.fileBrowser
		a.fileBrowserMu.Unlock()
		window.Show().Focus()
		return
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
		URL:                        "/?page=files&filesWindow=1",
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
