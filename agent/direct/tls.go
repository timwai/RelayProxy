package direct

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"

	"relayproxy/internal/cert"
)

func PinnedTLSConfig(fingerprint string) (*tls.Config, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return nil, errors.New("public direct certificate fingerprint is required")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCertificates [][]byte, _ [][]*x509.Certificate) error {
			return cert.VerifyFingerprint(rawCertificates, fingerprint)
		},
	}, nil
}
