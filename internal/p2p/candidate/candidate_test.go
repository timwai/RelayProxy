package candidate

import (
	"net/netip"
	"testing"

	"relayproxy/internal/protocol"
)

func TestValidateCandidates(t *testing.T) {
	items, err := Validate([]protocol.RDPCandidate{
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:3389", Priority: 10},
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:3389", Priority: 5},
		{Protocol: "udp", Type: "lan", Address: "[2001:db8::10]:3389", Priority: 20},
	})
	if err != nil || len(items) != 2 {
		t.Fatalf("candidate normalization failed: %v %#v", err, items)
	}
	if items[0].Address != "[2001:db8::10]:3389" {
		t.Fatalf("IPv6 candidate was not preserved or prioritized: %#v", items)
	}
	for _, invalid := range []protocol.RDPCandidate{
		{Protocol: "tcp", Type: "lan", Address: "example.com:3389"},
		{Protocol: "udp", Type: "lan", Address: "0.0.0.0:3389"},
		{Protocol: "udp", Type: "reflexive", Address: "127.0.0.1:3389"},
		{Protocol: "tcp", Type: "lan", Address: "192.0.2.10:0"},
		{Protocol: "udp", Type: "lan", Address: "[fe80::1]:3389"},
	} {
		if _, err := Validate([]protocol.RDPCandidate{invalid}); err == nil {
			t.Fatalf("invalid candidate accepted: %#v", invalid)
		}
	}
}

func TestDiscoveryPriorityMatchesDirectPathPreference(t *testing.T) {
	privateIPv4 := netip.MustParseAddr("192.168.1.10")
	privateIPv6 := netip.MustParseAddr("fd00::10")
	publicIPv6 := netip.MustParseAddr("2001:db8::10")
	publicIPv4 := netip.MustParseAddr("198.51.100.10")

	if got := discoveryPriority(privateIPv4, "udp"); got != 1200 {
		t.Fatalf("private IPv4 UDP priority=%d, want 1200", got)
	}
	if got := discoveryPriority(privateIPv6, "udp"); got != 1200 {
		t.Fatalf("private IPv6 UDP priority=%d, want 1200", got)
	}
	if got := discoveryPriority(publicIPv6, "udp"); got != 1100 {
		t.Fatalf("public IPv6 UDP priority=%d, want 1100", got)
	}
	if got := discoveryPriority(publicIPv4, "udp"); got != 1000 {
		t.Fatalf("public IPv4 UDP priority=%d, want 1000", got)
	}
	if got := discoveryPriority(privateIPv4, "tcp"); got != 1100 {
		t.Fatalf("private IPv4 TCP priority=%d, want 1100", got)
	}
}


func TestProbeWireCodecRoundTrip(t *testing.T) {
	const nonce uint64 = 0x0102030405060708
	request := EncodeProbeRequest(nonce)
	decodedNonce, ok := DecodeProbeRequest(request[:])
	if !ok || decodedNonce != nonce {
		t.Fatalf("request round trip: nonce=%x ok=%v", decodedNonce, ok)
	}

	observed := netip.MustParseAddrPort("198.51.100.7:4242")
	response, ok := EncodeProbeResponse(nonce, observed)
	if !ok {
		t.Fatal("failed to encode valid probe response")
	}
	decodedObserved, ok := DecodeProbeResponse(response[:], nonce)
	if !ok || decodedObserved != observed {
		t.Fatalf("response round trip: observed=%s ok=%v", decodedObserved, ok)
	}

	response[4]++
	if _, ok := DecodeProbeResponse(response[:], nonce); ok {
		t.Fatal("probe response with unsupported version was accepted")
	}
}
