package candidate

import (
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
