//go:build darwin

package gui

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"relayproxy/agent/bridge"
)

// Run opens the local Agent console using the system browser on macOS. The web
// management surface uses the same HTML/components as the Windows WebView2
// client. The caller keeps the Agent process alive headlessly after
// ErrExternalUI is returned.
func Run(b *bridge.UIBridge, opts Options) error {
	if b == nil {
		return fmt.Errorf("macOS UI: bridge is nil")
	}
	managementAddr := managementURL(b, opts)
	if managementAddr == "" {
		return fmt.Errorf("macOS UI requires the local web management page to be enabled")
	}
	if err := exec.Command("open", managementAddr).Start(); err != nil {
		return fmt.Errorf("open macOS management UI: %w", err)
	}
	log.Printf("[GUI] macOS 管理界面已打开: %s", managementURLForLog(managementAddr))
	return ErrExternalUI
}

func managementURLForLog(value string) string {
	redacted := redactManagementURL(value)
	if redacted == "" {
		return "<invalid management URL>"
	}
	return redacted
}

func managementURL(b *bridge.UIBridge, opts Options) string {
	url := strings.TrimSpace(opts.WebURL)
	if url != "" {
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		return url
	}
	return savedManagementURL(b)
}

func savedManagementURL(b *bridge.UIBridge) string {
	cfg := b.GetConfig()
	if !cfg.IsWebEnabled() {
		return ""
	}
	host := cfg.Web.Listen
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Web.Port)) + "/"
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
