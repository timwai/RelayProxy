package secure

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"time"
)

type TLSIdentity struct {
	Certificate tls.Certificate
	Fingerprint string
}

func GenerateEphemeralIdentity() (*TLSIdentity, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "RelayProxy P2P Ephemeral"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(2 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  privateKey,
		Leaf:        leaf,
	}
	return &TLSIdentity{Certificate: cert, Fingerprint: CertificateFingerprint(der)}, nil
}

func CertificateFingerprint(rawCertificate []byte) string {
	sum := sha256.Sum256(rawCertificate)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func VerifyPeerFingerprint(rawCerts [][]byte, expected string) error {
	if len(rawCerts) == 0 {
		return errors.New("P2P peer did not provide a certificate")
	}
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return errors.New("expected P2P certificate fingerprint is empty")
	}
	actual := strings.ToLower(CertificateFingerprint(rawCerts[0]))
	if actual != expected {
		return errors.New("P2P peer certificate fingerprint mismatch")
	}
	return nil
}
