package cert

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

func Fingerprint(rawCertificate []byte) string {
	sum := sha256.Sum256(rawCertificate)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func VerifyFingerprint(rawCertificates [][]byte, expected string) error {
	if len(rawCertificates) == 0 {
		return errors.New("peer did not provide a certificate")
	}
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return errors.New("expected certificate fingerprint is empty")
	}
	if strings.ToLower(Fingerprint(rawCertificates[0])) != expected {
		return errors.New("peer certificate fingerprint mismatch")
	}
	return nil
}
