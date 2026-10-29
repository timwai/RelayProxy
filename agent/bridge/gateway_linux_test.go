//go:build linux

package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relayproxy/agent/app"
	"relayproxy/agent/gateway"
)

func TestTransparentModeSavesDoNotTouchDisabledGateway(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Linux preflight requires root or net capabilities")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	logPath := filepath.Join(dir, "nft-calls")
	t.Setenv("RELAY_GATEWAY_TEST_LOG", logPath)
	for name, body := range map[string]string{
		"iptables": "exit 0\n",
		"nft":      "printf '%s\\n' \"$*\" >> \"$RELAY_GATEWAY_TEST_LOG\"\necho 'netlink: Error: cache initialization failed: Invalid argument' >&2\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	b := newTestBridge(t)
	cfg := b.agent.Config()
	_ = b.agent.Close()
	cfg.SOCKS5Enabled, cfg.HTTPEnabled = ptr(false), ptr(false)
	cfg.ServerAddress, cfg.TransportMode = "127.0.0.1", "tcp_only"
	cfg.TCPPort, cfg.QUICPort = 1, 1
	cfg.ConnectTimeout = 20 * time.Millisecond
	a, err := app.NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b.agent = a
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	// Exercise both directions of the switch with IPv4 only, a broken nft
	// kernel interface and no ip6tables executable. Neither save owns gateway.
	for _, mode := range []string{"divert", ""} {
		var update ConfigUpdate
		update.Network.Mode = ptr(mode)
		if _, err := b.SaveConfig(update); err != nil {
			t.Fatalf("saving transparent mode %q depended on the gateway/IPv6: %v", mode, err)
		}
	}
	if data, err := os.ReadFile(logPath); !os.IsNotExist(err) {
		t.Fatalf("unrelated saves called nft: %s, %v", data, err)
	}
	// Explicit gateway cleanup remains an independent operation and must
	// still surface its own kernel error instead of claiming success.
	if _, err := b.SaveConfig(ConfigUpdate{Gateway: &gateway.Config{}}); err == nil || !strings.Contains(err.Error(), "Invalid argument") {
		t.Fatalf("explicit gateway cleanup hid the kernel failure: %v", err)
	}
	if data, err := os.ReadFile(logPath); err != nil || len(data) == 0 {
		t.Fatalf("explicit cleanup did not reach nft: %s, %v", data, err)
	}
}
