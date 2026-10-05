package direct

import (
	"testing"

	"relayproxy/internal/cert"
	"relayproxy/internal/p2p/secure"
)

func TestPinnedTLSConfigAcceptsOnlyVerifiedExitCertificate(t *testing.T) {
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := cert.Fingerprint(identity.Certificate.Certificate[0])
	config, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion == 0 || config.VerifyPeerCertificate == nil {
		t.Fatalf("incomplete tls config: %+v", config)
	}
	if err := config.VerifyPeerCertificate([][]byte{identity.Certificate.Certificate[0]}, nil); err != nil {
		t.Fatal(err)
	}

	other, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.VerifyPeerCertificate([][]byte{other.Certificate.Certificate[0]}, nil); err == nil {
		t.Fatal("wrong exit certificate was accepted")
	}
}

func TestPinnedTLSConfigRejectsMissingFingerprint(t *testing.T) {
	if _, err := PinnedTLSConfig(""); err == nil {
		t.Fatal("missing fingerprint was accepted")
	}
}
