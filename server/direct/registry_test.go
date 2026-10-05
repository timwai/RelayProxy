package direct

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type registryTestTunnel struct {
	remote net.Addr
}

func (s *registryTestTunnel) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not used")
}
func (s *registryTestTunnel) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not used")
}
func (s *registryTestTunnel) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *registryTestTunnel) RemoteAddr() net.Addr            { return s.remote }
func (s *registryTestTunnel) LocalAddr() net.Addr             { return nil }
func (s *registryTestTunnel) Close() error                    { return nil }
func (s *registryTestTunnel) Done() <-chan struct{}           { return nil }

func registryExit(deviceID, remote string) *session.DeviceSession {
	return &session.DeviceSession{
		DeviceID: deviceID,
		Grants:   []string{protocol.CapabilityProxyExit},
		Tunnel: &registryTestTunnel{
			remote: &net.UDPAddr{IP: net.ParseIP(remote), Port: 443},
		},
	}
}

func TestRegistryPublishesOnlyVerifiedEndpoints(t *testing.T) {
	registry := NewRegistry(time.Minute)
	exit := registryExit("exit-1", "8.8.4.4")
	secret := bytes.Repeat([]byte{0x11}, 32)
	targets, err := registry.Register(exit, "reg-1", secret, 35820, []protocol.PublicDirectEndpoint{{
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  "[2606:4700:4700::1111]:35820",
		Source:   protocol.PublicDirectEndpointSourceIPv6,
	}, {
		Protocol: protocol.PublicDirectEndpointProtocolUDP,
		Address:  "exit.example.com:443",
		Source:   protocol.PublicDirectEndpointSourceManual,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("verification targets = %d, want observed + ipv6 + manual", len(targets))
	}
	if got := registry.VerifiedEndpoints(exit.DeviceID); len(got) != 0 {
		t.Fatalf("unverified endpoints published: %#v", got)
	}

	for _, target := range targets {
		if !registry.MarkVerifying(target) || !registry.MarkVerified(target) {
			t.Fatalf("failed to mark target verified: %#v", target.Endpoint)
		}
	}
	verified := registry.VerifiedEndpoints(exit.DeviceID)
	if len(verified) != 3 {
		t.Fatalf("verified endpoints = %d, want 3", len(verified))
	}
	for _, endpoint := range verified {
		if !endpoint.Verified || endpoint.VerifiedAt == 0 || endpoint.ExpiresAt == 0 {
			t.Fatalf("incomplete verified endpoint: %#v", endpoint)
		}
	}
}

func TestRegistryRejectsUnusableReportedEndpoints(t *testing.T) {
	registry := NewRegistry(time.Minute)
	exit := registryExit("exit-1", "10.0.0.8")
	secret := bytes.Repeat([]byte{0x11}, 32)
	tests := []protocol.PublicDirectEndpoint{
		{Protocol: "tcp", Address: "8.8.8.8:35820", Source: protocol.PublicDirectEndpointSourceManual},
		{Protocol: "udp", Address: "10.0.0.8:35820", Source: protocol.PublicDirectEndpointSourceManual},
		{Protocol: "udp", Address: "[fd00::1]:35820", Source: protocol.PublicDirectEndpointSourceIPv6},
		{Protocol: "udp", Address: "[2606:4700:4700::1111]:35821", Source: protocol.PublicDirectEndpointSourceIPv6},
		{Protocol: "udp", Address: "8.8.8.8:35820", Source: protocol.PublicDirectEndpointSourceObserved},
	}
	for _, endpoint := range tests {
		if _, err := registry.Register(exit, "reg-1", secret, 35820, []protocol.PublicDirectEndpoint{endpoint}); err == nil {
			t.Fatalf("invalid endpoint was accepted: %#v", endpoint)
		}
	}
}

func TestRegistryIgnoresStaleVerificationAfterReregister(t *testing.T) {
	registry := NewRegistry(time.Minute)
	exit := registryExit("exit-1", "8.8.8.8")
	secret := bytes.Repeat([]byte{0x33}, 32)
	first, err := registry.Register(exit, "same-reg-id", secret, 35820, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.Register(exit, "same-reg-id", secret, 35820, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Generation == second[0].Generation {
		t.Fatal("registration generation did not advance")
	}
	if registry.MarkVerified(first[0]) {
		t.Fatal("stale verification result modified current registration")
	}
	if !registry.MarkVerified(second[0]) {
		t.Fatal("current verification result was rejected")
	}
}

func TestRegistryInvalidatesOnlyMatchingSessionGeneration(t *testing.T) {
	registry := NewRegistry(time.Minute)
	firstSession := registryExit("exit-1", "8.8.8.8")
	secondSession := registryExit("exit-1", "8.8.8.8")
	secret := bytes.Repeat([]byte{0x44}, 32)

	if _, err := registry.Register(firstSession, "reg-1", secret, 35820, nil); err != nil {
		t.Fatal(err)
	}
	if !registry.InvalidateSession(firstSession) {
		t.Fatal("matching session did not invalidate registration")
	}

	if _, err := registry.Register(secondSession, "reg-2", secret, 35820, nil); err != nil {
		t.Fatal(err)
	}
	if registry.InvalidateSession(firstSession) {
		t.Fatal("stale session invalidated a newer registration")
	}
	if _, ok := registry.Snapshot("exit-1"); !ok {
		t.Fatal("new registration disappeared")
	}
}

func TestRegistryExpiresPublishedEndpoints(t *testing.T) {
	registry := NewRegistry(20 * time.Millisecond)
	exit := registryExit("exit-1", "8.8.8.8")
	secret := bytes.Repeat([]byte{0x55}, 32)
	targets, err := registry.Register(exit, "reg-1", secret, 35820, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.MarkVerified(targets[0]) {
		t.Fatal("failed to mark endpoint verified")
	}
	if len(registry.VerifiedEndpoints(exit.DeviceID)) != 1 {
		t.Fatal("verified endpoint was not published")
	}
	time.Sleep(30 * time.Millisecond)
	if got := registry.VerifiedEndpoints(exit.DeviceID); len(got) != 0 {
		t.Fatalf("expired endpoint remained published: %#v", got)
	}
	snapshot, ok := registry.Snapshot(exit.DeviceID)
	if !ok || len(snapshot.Endpoints) != 1 || snapshot.Endpoints[0].State != StateExpired {
		t.Fatalf("expired state not reflected in snapshot: %#v", snapshot)
	}
}
