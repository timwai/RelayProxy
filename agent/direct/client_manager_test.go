package direct

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestSelectPublicEndpointUsesStablePreference(t *testing.T) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	path := protocol.ProxyPublicDirectPath{
		Available: true,
		Transport: "quic",
		Endpoints: []protocol.PublicDirectEndpoint{
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "[2001:4860:4860::8888]:35820", Source: protocol.PublicDirectEndpointIPv6, Verified: true, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "203.0.113.20:35820", Source: protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "exit.example.com:35820", Source: protocol.PublicDirectEndpointManual, Verified: true, CertFingerprint: fingerprint},
		},
	}
	got, ok := selectPublicEndpoint(path)
	if !ok {
		t.Fatal("no public direct endpoint selected")
	}
	if got.Source != protocol.PublicDirectEndpointManual || got.Address != "exit.example.com:35820" {
		t.Fatalf("selected endpoint=%+v, want verified manual endpoint", got)
	}

	path.Endpoints = path.Endpoints[:2]
	got, ok = selectPublicEndpoint(path)
	if !ok || got.Source != protocol.PublicDirectEndpointObserved {
		t.Fatalf("selected endpoint=%+v, want observed IPv4 before IPv6", got)
	}
}

func TestSelectPublicEndpointIgnoresUnverifiedHigherPriority(t *testing.T) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	path := protocol.ProxyPublicDirectPath{
		Available: true,
		Transport: "quic",
		Endpoints: []protocol.PublicDirectEndpoint{
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "exit.example.com:35820", Source: protocol.PublicDirectEndpointManual, Verified: false, CertFingerprint: fingerprint},
			{Protocol: protocol.PublicDirectEndpointProtocolUDP, Address: "203.0.113.20:35820", Source: protocol.PublicDirectEndpointObserved, Verified: true, CertFingerprint: fingerprint},
		},
	}
	got, ok := selectPublicEndpoint(path)
	if !ok || got.Source != protocol.PublicDirectEndpointObserved {
		t.Fatalf("selected endpoint=%+v, want verified observed endpoint", got)
	}
}

type clientManagerTestSession struct {
	once sync.Once
	done chan struct{}
}

func newClientManagerTestSession() *clientManagerTestSession {
	return &clientManagerTestSession{done: make(chan struct{})}
}

func (s *clientManagerTestSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *clientManagerTestSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *clientManagerTestSession) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *clientManagerTestSession) RemoteAddr() net.Addr            { return &net.UDPAddr{} }
func (s *clientManagerTestSession) LocalAddr() net.Addr             { return &net.UDPAddr{} }

func (s *clientManagerTestSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *clientManagerTestSession) Done() <-chan struct{} { return s.done }

func TestClientManagerClosesReadySessionWhenAuthorizationDisappears(t *testing.T) {
	fake := newClientManagerTestSession()
	manager := NewClientManager(context.Background(), func() string { return "client" }, ClientManagerOptions{
		AttemptTimeout: time.Second,
		Dial: func(context.Context, DialConfig) (tunnel.TunnelSession, error) {
			return fake, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available: true, Transport: "quic",
			Ticket:          []byte("one-time-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol:        protocol.PublicDirectEndpointProtocolUDP,
				Address:         "203.0.113.20:35820",
				Source:          protocol.PublicDirectEndpointObserved,
				Verified:        true,
				CertFingerprint: fingerprint,
			}},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct client attempt was not started")
	}

	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}

	manager.UpdateInventory(nil)
	select {
	case <-fake.Done():
	case <-time.After(time.Second):
		t.Fatal("authorization removal did not close the ready public direct session")
	}
	if _, ok := manager.ReadyForExit("exit"); ok {
		t.Fatal("revoked public direct session remained selectable")
	}
}
