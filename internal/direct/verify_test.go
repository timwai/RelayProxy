package direct

import (
	"bytes"
	"testing"
)

func TestVerificationProofBindsRegistrationAndNonce(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, VerificationSecretSize)
	nonce := bytes.Repeat([]byte{0x24}, VerificationNonceSize)
	proof, err := VerificationProof(secret, "registration-1", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyProof(secret, "registration-1", nonce, proof) {
		t.Fatal("valid verification proof was rejected")
	}
	if VerifyProof(secret, "registration-2", nonce, proof) {
		t.Fatal("proof was not bound to registration id")
	}
	changedNonce := append([]byte(nil), nonce...)
	changedNonce[0] ^= 0xff
	if VerifyProof(secret, "registration-1", changedNonce, proof) {
		t.Fatal("proof was not bound to nonce")
	}
	changedSecret := append([]byte(nil), secret...)
	changedSecret[0] ^= 0xff
	if VerifyProof(changedSecret, "registration-1", nonce, proof) {
		t.Fatal("proof was not bound to secret")
	}
}

func TestVerificationProofRejectsMalformedInput(t *testing.T) {
	secret := bytes.Repeat([]byte{0x42}, VerificationSecretSize)
	if _, err := VerificationProof(secret[:len(secret)-1], "registration-1", bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("short secret was accepted")
	}
	if _, err := VerificationProof(secret, "", bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Fatal("empty registration id was accepted")
	}
	if _, err := VerificationProof(secret, "registration-1", []byte{1}); err == nil {
		t.Fatal("short nonce was accepted")
	}
}
