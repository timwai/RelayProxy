package direct

import (
	"testing"

	"relayproxy/internal/cert"
)

func TestPinnedTLSConfigVerifiesRegisteredFingerprint(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "public-direct-pin-test")
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := cert.Fingerprint(certificate.Certificate[0])
	config, err := PinnedTLSConfig(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion == 0 || config.VerifyPeerCertificate == nil {
		t.Fatal("pinned TLS config is incomplete")
	}
	if err := config.VerifyPeerCertificate(certificate.Certificate, nil); err != nil {
		t.Fatalf("correct fingerprint rejected: %v", err)
	}

	other, err := cert.EnsureCertificate("", "", "public-direct-pin-other")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.VerifyPeerCertificate(other.Certificate, nil); err == nil {
		t.Fatal("wrong certificate fingerprint was accepted")
	}
	if _, err := PinnedTLSConfig(""); err == nil {
		t.Fatal("empty fingerprint was accepted")
	}
}
