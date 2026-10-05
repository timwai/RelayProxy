package direct

import (
	"testing"

	"relayproxy/internal/protocol"
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
