package direct

import (
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
)

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
