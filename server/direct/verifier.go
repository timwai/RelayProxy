package direct

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	defaultVerifyTimeout = 3 * time.Second
	defaultVerifyTTL     = 5 * time.Minute
)

type endpointResolver func(context.Context, string) (string, error)

type Verifier struct {
	Registry *Registry
	Timeout  time.Duration
	TTL      time.Duration
	resolve  endpointResolver
}

func (v *Verifier) Verify(ctx context.Context, deviceID, sessionID, address string) error {
	if v == nil || v.Registry == nil {
		return errors.New("public direct verifier registry is required")
	}
	record, ok := v.Registry.Lookup(deviceID, sessionID, address)
	if !ok {
		return errors.New("public direct endpoint registration is stale")
	}
	if !v.Registry.markVerifyingRegistration(record) {
		return errors.New("public direct endpoint registration changed")
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resolve := v.resolve
	if resolve == nil {
		resolve = resolvePublicEndpoint
	}
	dialAddress, err := resolve(verifyCtx, record.Endpoint.Address)
	if err == nil {
		err = probeEndpoint(verifyCtx, record, dialAddress)
	}
	if err != nil {
		v.Registry.markFailedRegistration(record, err)
		return err
	}

	ttl := v.TTL
	if ttl <= 0 {
		ttl = defaultVerifyTTL
	}
	if !v.Registry.markVerifiedRegistration(record, dialAddress, ttl) {
		return errors.New("public direct endpoint changed during verification")
	}
	return nil
}

func resolvePublicEndpoint(ctx context.Context, address string) (string, error) {
	return resolvePublicEndpointWithLookup(ctx, address, func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	})
}

func resolvePublicEndpointWithLookup(
	ctx context.Context,
	address string,
	lookup func(context.Context, string) ([]netip.Addr, error),
) (string, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return "", fmt.Errorf("invalid public direct endpoint %q", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid public direct endpoint port %q", portText)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if !isPublicIP(ip) {
			return "", fmt.Errorf("public direct endpoint %q resolved to non-public address", address)
		}
		return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
	}
	if lookup == nil {
		return "", errors.New("public direct endpoint resolver is unavailable")
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return "", fmt.Errorf("resolve public direct endpoint %q: %w", host, err)
	}
	for _, ip := range addrs {
		ip = ip.Unmap()
		if isPublicIP(ip) {
			// Dial the verified public IP literal directly so a second DNS
			// lookup cannot rebind the Server probe onto a private address.
			return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
		}
	}
	return "", fmt.Errorf("public direct endpoint %q has no globally routable address", host)
}

func probeEndpoint(ctx context.Context, record EndpointRecord, dialAddress string) error {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			return cert.VerifyFingerprint(rawCerts, record.CertFingerprint)
		},
	}
	session, err := tunnel.DialDirectQUIC(ctx, dialAddress, tlsConfig, nil)
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
	// Signal that the verifier has no more request bytes. The Exit waits for
	// this FIN after writing its response before closing the QUIC connection,
	// preventing connection close from overtaking the response stream.
	if err := stream.CloseWrite(); err != nil {
		return fmt.Errorf("public direct probe close-write failed: %w", err)
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
