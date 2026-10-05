package direct

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	VerificationSecretSize = 32
	VerificationNonceSize  = 32
)

func VerificationProof(secret []byte, registrationID string, nonce []byte) ([]byte, error) {
	if len(secret) != VerificationSecretSize {
		return nil, errors.New("Public Direct verification secret has invalid size")
	}
	if registrationID == "" {
		return nil, errors.New("Public Direct registration id is required")
	}
	if len(nonce) < 16 || len(nonce) > 64 {
		return nil, errors.New("Public Direct verification nonce has invalid size")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("RelayProxy Public Direct verification\x00"))
	writeVerificationField(mac, []byte(registrationID))
	writeVerificationField(mac, nonce)
	return mac.Sum(nil), nil
}

func VerifyProof(secret []byte, registrationID string, nonce, proof []byte) bool {
	expected, err := VerificationProof(secret, registrationID, nonce)
	return err == nil && hmac.Equal(expected, proof)
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeVerificationField(out hashWriter, value []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = out.Write(length[:])
	_, _ = out.Write(value)
}

// EqualVerificationSecret is used by registries that need constant-time secret
// equality without exposing the secret to logs or status snapshots.
func EqualVerificationSecret(a, b []byte) bool {
	if len(a) != VerificationSecretSize || len(b) != VerificationSecretSize {
		return false
	}
	return hmac.Equal(a, b)
}

// CloneSecret returns an independent in-memory copy so callers cannot mutate a
// registered verification key after publication.
func CloneSecret(secret []byte) []byte {
	return bytes.Clone(secret)
}
