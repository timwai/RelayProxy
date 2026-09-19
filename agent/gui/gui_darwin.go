//go:build darwin

package gui

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"

	"relayproxy/agent/bridge"
)

// Run opens the local Agent console using the system browser on macOS. The web
// management surface is loopback-only and uses the same HTML/components as the
// Windows WebView2 client. The caller keeps the Agent process alive headlessly
// after ErrExternalUI is returned.
func Run(b *bridge.UIBridge, _ Options) error {
	if b == nil {
		return fmt.Errorf("macOS UI: bridge is nil")
	}
	cfg := b.GetConfig()
	if !cfg.IsWebEnabled() {
		return fmt.Errorf("macOS UI requires the local web management page to be enabled")
	}
	host := cfg.Web.Listen
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	url := "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Web.Port)) + "/"
	if err := exec.Command("open", url).Start(); err != nil {
		return fmt.Errorf("open macOS management UI: %w", err)
	}
	log.Printf("[GUI] macOS 管理界面已打开: %s", url)
	return ErrExternalUI
}

func ShowStartupError(err error, fallbackURL string) {
	if err == nil || fallbackURL == "" {
		return
	}
	_ = exec.Command("open", fallbackURL).Start()
}

func ActivateExistingWindow() {}
func RequestQuit()            {}
func RequestRestart() error   { return ErrUnsupported }
func WaitForRestartParent()   {}
