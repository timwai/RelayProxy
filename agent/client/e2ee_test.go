package client

import (
	"crypto/ed25519"
	"testing"

	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
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
	if dialer.shouldUseE2EE("exit-device", protocol.ProxyPathP2PQUIC) {
		t.Fatal("direct P2P path unexpectedly selected the Relay E2EE wrapper")
	}
	if dialer.shouldUseE2EE(protocol.ServerExitDeviceID, protocol.ProxyPathRelayTLS) {
		t.Fatal("Server-local Exit unexpectedly selected the Relay E2EE wrapper")
	}
}
