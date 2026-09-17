package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/config"
)

// loadServerTLS prepares the one certificate used by the admin, TCP and QUIC
// listeners. Relative paths retain their working-directory semantics.
func loadServerTLS(cfg *config.ServerConfig) (*tls.Config, error) {
	if !cfg.NeedsCertificate() {
		log.Println("[Server] TLS is disabled for both the admin and tunnels; configured certificate paths are not loaded.")
		return nil, nil
	}

	certPath, keyPath := cfg.Server.CertFile, cfg.Server.KeyFile
	for _, path := range []*string{&certPath, &keyPath} {
		if *path == "" {
			continue
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return nil, fmt.Errorf("resolve certificate path %q: %w", *path, err)
		}
		*path = absolute
	}
	certificate, err := cert.EnsureCertificate(certPath, keyPath, "relayproxy.local")
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse server TLS certificate: %w", err)
	}
	if certPath == "" {
		log.Println("[Cert] No server.cert_file/server.key_file configured; using an ephemeral self-signed development certificate.")
	} else {
		log.Printf("[Cert] Loaded certificate=%q private_key=%q", certPath, keyPath)
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	log.Printf("[Cert] subject=%q issuer=%q expires=%s sha256=%x", leaf.Subject.String(), leaf.Issuer.String(), leaf.NotAfter.UTC().Format(time.RFC3339), fingerprint)
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}, nil
}

func tunnelTLSConfig(cfg *config.ServerConfig, certificate *tls.Config) *tls.Config {
	if !cfg.IsTLSEnabled() {
		return nil
	}
	return certificate
}

func adminTLSConfig(cfg *config.ServerConfig, certificate *tls.Config) *tls.Config {
	if !cfg.IsAdminTLSEnabled() {
		return nil
	}
	return certificate
}
