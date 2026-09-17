//go:build !windows

package gui

import (
	"log"

	"relayproxy/agent/bridge"
)

// Run reports that the desktop window is unavailable. The agent itself keeps
// working headlessly on these platforms; only the window and tray are Windows-only.
func Run(b *bridge.UIBridge, opts Options) error {
	log.Println("[GUI] 桌面窗口仅在 Windows 上可用，将以无界面模式运行。")
	return ErrUnsupported
}

// ActivateExistingWindow is a no-op on platforms without a desktop window.
func ActivateExistingWindow() {}

// RequestQuit is a no-op when there is no native desktop event loop. Headless
// process lifetime is handled directly by the web server's Done channel.
func RequestQuit() {}

// RequestRestart is unavailable without the native desktop process wrapper.
func RequestRestart() error { return ErrUnsupported }

// WaitForRestartParent is a no-op on platforms without the Windows launcher.
func WaitForRestartParent() {}
