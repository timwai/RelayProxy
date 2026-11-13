package client

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestShouldUseE2EERequiresPersistentPeerPinVerifier(t *testing.T) {
	dialer := NewTunnelDialer(nil, nil)
	identity := e2ee.Identity{
		PublicKey:  make(ed25519.PublicKey, ed25519.PublicKeySize),
		PrivateKey: make(ed25519.PrivateKey, ed25519.PrivateKeySize),
	}
	getPeerKey := func(string) ([]byte, bool) {
		return make([]byte, ed25519.PublicKeySize), true
	}
	dialer.ConfigureE2EE(identity, getPeerKey, nil)
	dialer.SetE2EEServerSupported(true)
	if dialer.shouldUseE2EE("exit-device", protocol.ProxyPathRelayTLS) {
		t.Fatal("Relay E2EE enabled without a persistent peer pin verifier")
	}

	dialer.ConfigureE2EE(identity, getPeerKey, func(string, []byte) error { return nil })
	if !dialer.shouldUseE2EE("exit-device", protocol.ProxyPathRelayTLS) {
		t.Fatal("Relay E2EE was not enabled when both peer key and pin verifier are available")
	}
	if !dialer.shouldUseE2EE("exit-device", protocol.ProxyPathP2PQUIC) {
		t.Fatal("authenticated P2P QUIC path did not select the E2EE wrapper")
	}
	if dialer.shouldUseE2EE("exit-device", protocol.ProxyPathPublicDirectQUIC) {
		t.Fatal("Public Direct unexpectedly selected the unsupported E2EE wrapper")
	}
	dialer.SetE2EEServerSupported(false)
	if dialer.shouldUseE2EE("exit-device", protocol.ProxyPathP2PQUIC) == false {
		t.Fatal("direct P2P E2EE incorrectly depended on Relay Server SecureRelay support")
	}
	if dialer.shouldUseE2EE("exit-device", protocol.ProxyPathRelayTLS) {
		t.Fatal("Relay E2EE activated without Relay Server SecureRelay support")
	}
	if dialer.shouldUseE2EE(protocol.ServerExitDeviceID, protocol.ProxyPathRelayTLS) {
		t.Fatal("Server-local Exit unexpectedly selected the Relay E2EE wrapper")
	}
}

func TestRequiredE2EEFailsClosedForLegacyAndServerLocalExits(t *testing.T) {
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return &namedSession{name: "relay"} }, nil)
	dialer.ConfigureE2EEMode("required")
	for _, exitID := range []string{"", protocol.ServerExitDeviceID, "legacy-exit"} {
		if _, err := dialer.selectProxyDataSession(exitID); err == nil {
			t.Fatalf("required E2EE selected legacy or non-blind Exit %q", exitID)
		}
		if _, err := dialer.DialTCP(context.Background(), exitID, "secret.example", 443); err == nil {
			t.Fatalf("required E2EE allowed plaintext TCP to Exit %q", exitID)
		}
		if _, err := dialer.DialUDP(context.Background(), exitID, "secret.example", 53); err == nil {
			t.Fatalf("required E2EE allowed plaintext UDP to Exit %q", exitID)
		}
	}
}

func TestRequiredE2EEUsesAuthenticatedP2PAndNeverDowngrades(t *testing.T) {
	relay := &namedSession{name: "relay"}
	direct := &namedSession{name: "direct"}
	directPath := protocol.ProxyPathP2PQUIC
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, func() string { return "client" })
	dialer.ConfigureDirectPolicy("auto", true)
	dialer.ConfigurePathProvider(func(string) (SelectedSession, bool) {
		return SelectedSession{Session: direct, Path: directPath}, true
	}, nil)
	dialer.ConfigureE2EE(e2ee.Identity{
		PublicKey:  make(ed25519.PublicKey, ed25519.PublicKeySize),
		PrivateKey: make(ed25519.PrivateKey, ed25519.PrivateKeySize),
	}, func(string) ([]byte, bool) {
		return make([]byte, ed25519.PublicKeySize), true
	}, func(string, []byte) error { return nil })
	dialer.SetE2EEServerSupported(true)
	dialer.ConfigureE2EEMode("required")

	selected, err := dialer.selectProxyDataSession("exit")
	if err != nil || selected.Session != direct || selected.Path != protocol.ProxyPathP2PQUIC {
		t.Fatalf("required E2EE did not use authenticated P2P QUIC: selected=%+v err=%v", selected, err)
	}
	if err := dialer.requireSecureProxyPath("exit", selected.Path); err != nil {
		t.Fatalf("required E2EE rejected authenticated P2P QUIC: %v", err)
	}
	if err := dialer.requireSecureProxyPath("exit", protocol.ProxyPathPublicDirectQUIC); err == nil ||
		!strings.Contains(err.Error(), "Public Direct") {
		t.Fatalf("required E2EE accepted unsupported Public Direct path: %v", err)
	}

	// When Public Direct is the selected path, auto mode must fall back to an
	// E2EE-capable Relay rather than use the unsupported direct handler.
	directPath = protocol.ProxyPathPublicDirectQUIC
	selected, err = dialer.selectProxyDataSession("exit")
	if err != nil || selected.Session != relay || !selected.Path.IsRelay() {
		t.Fatalf("required E2EE did not fall back from Public Direct to Relay: selected=%+v err=%v", selected, err)
	}

	dialer.SetE2EEServerSupported(false)
	if _, err := dialer.selectProxyDataSession("exit"); err == nil {
		t.Fatal("required E2EE silently fell back after Relay capability was removed")
	}
	dialer.SetE2EEServerSupported(true)
	dialer.ConfigureE2EEMode("off")
	if dialer.shouldUseE2EE("exit", protocol.ProxyPathP2PQUIC) {
		t.Fatal("explicit off mode unexpectedly initiated E2EE")
	}
	dialer.ConfigureE2EEMode("opportunistic")
	if !dialer.shouldUseE2EE("exit", protocol.ProxyPathP2PQUIC) {
		t.Fatal("opportunistic E2EE did not activate on a capable P2P peer")
	}
}
