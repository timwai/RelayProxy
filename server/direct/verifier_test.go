package direct

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	agentdirect "relayproxy/agent/direct"
	"relayproxy/internal/cert"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type expiredProbeAck struct{ *testControlStream }

func (s expiredProbeAck) CloseWrite() error { return context.DeadlineExceeded }

func TestValidProbeSurvivesAcknowledgementDeadline(t *testing.T) {
	nonce := bytes.Repeat([]byte{3}, 32)
	stream := expiredProbeAck{newControlStream(t, protocol.PublicDirectHandshakeResponse{
		Success: true, Probe: &protocol.PublicDirectProbeResponse{Nonce: nonce},
	})}
	if err := exchangeProbe(stream, nonce); err != nil {
		t.Fatalf("validated response rejected during FIN cleanup: %v", err)
	}
	wrong := expiredProbeAck{newControlStream(t, protocol.PublicDirectHandshakeResponse{
		Success: true, Probe: &protocol.PublicDirectProbeResponse{Nonce: []byte("wrong")},
	})}
	if err := exchangeProbe(wrong, nonce); err == nil {
		t.Fatal("invalid response was accepted")
	}
}

func startProbeListener(t *testing.T) (*agentdirect.PublicListener, string, context.CancelFunc) {
	t.Helper()
	certificate, err := cert.EnsureCertificate("", "", "public-direct-test")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := agentdirect.Listen(agentdirect.ListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     &tls.Config{Certificates: []tls.Certificate{certificate}},
		Authenticator: agentdirect.AuthenticatorFunc(func(context.Context, protocol.PublicDirectAuthRequest) error {
			return errors.New("proxy authentication is not used by verification probes")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = listener.Accept(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		<-done
	})
	return listener, cert.Fingerprint(certificate.Certificate[0]), cancel
}

func TestProbeReadHonorsVerificationDeadline(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "silent-probe-test")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tunnel.ListenDirectQUIC("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverCtx, cancelServer := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelServer()
	requestRead := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		session, err := listener.Accept(serverCtx)
		if err != nil {
			return
		}
		defer session.Close()
		stream, err := session.AcceptStream(serverCtx)
		if err != nil {
			return
		}
		defer stream.Close()
		var request protocol.PublicDirectHandshakeRequest
		if err := protocol.ReadJSON(stream, &request); err != nil {
			return
		}
		close(requestRead)
		// Keep QUIC alive but never answer the application-level challenge.
		select {
		case <-session.Done():
		case <-serverCtx.Done():
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = probeEndpoint(ctx, EndpointRecord{CertFingerprint: cert.Fingerprint(certificate.Certificate[0])}, listener.Addr().String())
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("silent probe ignored verification timeout: %v after %s", err, time.Since(started))
	}
	select {
	case <-requestRead:
	default:
		t.Fatal("probe failed before reaching the silent application")
	}
	cancelServer()
	<-serverDone
}

func TestVerifierPinsExitCertificateAndChallenge(t *testing.T) {
	listener, fingerprint, _ := startProbeListener(t)
	registry := NewRegistry()
	now := time.Now()
	const advertised = "exit.example.com:35820"
	registry.records["exit"] = map[string]EndpointRecord{
		advertised: {
			DeviceID: "exit", SessionID: "session-1", CertFingerprint: fingerprint,
			Endpoint: protocol.PublicDirectEndpoint{
				Protocol: protocol.PublicDirectEndpointProtocolUDP,
				Address:  advertised,
				Source:   protocol.PublicDirectEndpointManual,
			},
			State: StateUnknown, RegisteredAt: now,
		},
	}
	verifier := &Verifier{
		Registry: registry, Timeout: 2 * time.Second, TTL: time.Minute,
		resolve: func(context.Context, string) (string, error) { return listener.Addr(), nil },
	}
	if err := verifier.Verify(context.Background(), "exit", "session-1", advertised); err != nil {
		t.Fatal(err)
	}
	record, ok := registry.Lookup("exit", "session-1", advertised)
	if !ok || record.State != StateVerified || !record.Endpoint.Verified {
		t.Fatalf("verified record=%+v ok=%v", record, ok)
	}
	if record.Endpoint.Address != advertised || record.Endpoint.DialAddress != listener.Addr() {
		t.Fatalf("verified endpoint did not bind resolved dial target: %+v", record.Endpoint)
	}
	got := registry.VerifiedEndpoints("exit")
	if len(got) != 1 || got[0].Address != advertised || got[0].DialAddress != listener.Addr() {
		t.Fatalf("published endpoints=%+v", got)
	}
}

func TestVerifierRejectsWrongCertificateFingerprint(t *testing.T) {
	listener, _, _ := startProbeListener(t)
	registry := NewRegistry()
	registry.records["exit"] = map[string]EndpointRecord{
		listener.Addr(): {
			DeviceID: "exit", SessionID: "session-1",
			CertFingerprint: "sha256:" + strings.Repeat("0", 64),
			Endpoint: protocol.PublicDirectEndpoint{
				Protocol: protocol.PublicDirectEndpointProtocolUDP,
				Address:  listener.Addr(),
				Source:   protocol.PublicDirectEndpointManual,
			},
			State: StateUnknown, RegisteredAt: time.Now(),
		},
	}
	verifier := &Verifier{
		Registry: registry, Timeout: time.Second, TTL: time.Minute,
		resolve: func(context.Context, string) (string, error) { return listener.Addr(), nil },
	}
	if err := verifier.Verify(context.Background(), "exit", "session-1", listener.Addr()); err == nil {
		t.Fatal("wrong certificate fingerprint was accepted")
	}
	record, ok := registry.Lookup("exit", "session-1", listener.Addr())
	if !ok || record.State != StateFailed || record.Endpoint.Verified {
		t.Fatalf("failed record=%+v ok=%v", record, ok)
	}
}

func TestResolvePublicEndpointRejectsPrivateResolution(t *testing.T) {
	for _, address := range []string{"10.0.0.1:35820", "127.0.0.1:35820", "[::1]:35820", "[fe80::1]:35820"} {
		if _, err := resolvePublicEndpointWithLookup(context.Background(), address, nil); err == nil {
			t.Fatalf("non-public endpoint %q was accepted", address)
		}
	}

	resolved, err := resolvePublicEndpointWithLookup(
		context.Background(),
		"exit.example.com:35820",
		func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{
				netip.MustParseAddr("10.0.0.5"),
				netip.MustParseAddr("1.1.1.1"),
			}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "1.1.1.1:35820" {
		t.Fatalf("resolved endpoint=%q", resolved)
	}

	if _, err := resolvePublicEndpointWithLookup(
		context.Background(),
		"internal.example:35820",
		func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.168.1.5")}, nil
		},
	); err == nil {
		t.Fatal("private-only DNS result was accepted")
	}
}
