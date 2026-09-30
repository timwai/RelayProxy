package secure

import "testing"

func TestEphemeralTLSIdentityFingerprint(t *testing.T) {
	identity, err := GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity == nil || identity.Fingerprint == "" || len(identity.Certificate.Certificate) != 1 {
		t.Fatalf("incomplete identity: %#v", identity)
	}
	if err := VerifyPeerFingerprint(identity.Certificate.Certificate, identity.Fingerprint); err != nil {
		t.Fatalf("fingerprint verification failed: %v", err)
	}
	if err := VerifyPeerFingerprint(identity.Certificate.Certificate, "sha256:deadbeef"); err == nil {
		t.Fatal("wrong fingerprint was accepted")
	}
}
