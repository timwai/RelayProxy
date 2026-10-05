package direct

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	defaultVerifyTimeout = 3 * time.Second
	defaultVerifyTTL     = 5 * time.Minute
)

type Verifier struct {
	Registry *Registry
	Timeout  time.Duration
	TTL      time.Duration
}

func (v *Verifier) Verify(ctx context.Context, deviceID, sessionID, address string) error {
	if v == nil || v.Registry == nil {
		return errors.New("public direct verifier registry is required")
	}
	record, ok := v.Registry.Lookup(deviceID, sessionID, address)
	if !ok {
		return errors.New("public direct endpoint registration is stale")
	}
	if !v.Registry.MarkVerifying(deviceID, sessionID, address) {
		return errors.New("public direct endpoint registration changed")
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := probeEndpoint(verifyCtx, record)
	if err != nil {
		v.Registry.MarkFailed(deviceID, sessionID, address, err)
		return err
	}
	ttl := v.TTL
	if ttl <= 0 {
		ttl = defaultVerifyTTL
	}
	if !v.Registry.MarkVerified(deviceID, sessionID, address, ttl) {
		return errors.New("public direct endpoint changed during verification")
	}
	return nil
}

func probeEndpoint(ctx context.Context, record EndpointRecord) error {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return cert.VerifyFingerprint(rawCerts, record.CertFingerprint)
		},
	}
	session, err := tunnel.DialDirectQUIC(ctx, record.Endpoint.Address, tlsConfig, nil)
	if err != nil {
		return fmt.Errorf("public direct QUIC probe failed: %w", err)
	}
	defer session.Close()

	stream, err := session.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("public direct probe stream failed: %w", err)
	}
	defer stream.Close()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshakeRequest{
		Type:  protocol.PublicDirectHandshakeProbe,
		Probe: &protocol.PublicDirectProbeRequest{Nonce: nonce},
	}); err != nil {
		return err
	}
	var response protocol.PublicDirectHandshakeResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return err
	}
	if !response.Success || response.Probe == nil || !bytes.Equal(response.Probe.Nonce, nonce) {
		return errors.New("public direct verification challenge mismatch")
	}
	return nil
}
