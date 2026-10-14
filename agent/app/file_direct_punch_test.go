package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestFilePunchInvalidReflexiveDoesNotDiscardValidLocal(t *testing.T) {
	local := []protocol.P2PCandidate{
		{Protocol: "udp", Type: "lan", Address: "192.168.1.25:21000", Priority: 1200},
	}
	reflexive := []protocol.P2PCandidate{
		{Protocol: "udp", Type: "reflexive", Address: "127.0.0.1:23000", Priority: 800},
		{Protocol: "udp", Type: "reflexive", Address: "8.8.8.8:23456", Priority: 800},
		{Protocol: "udp", Type: "reflexive", Address: "224.0.0.1:23000", Priority: 800},
	}
	got := mergeFilePunchCandidates(local, reflexive)
	if len(got) != 2 || got[0].Address != local[0].Address || got[1].Address != "8.8.8.8:23456" {
		t.Fatalf("invalid reflections must be filtered individually: %+v", got)
	}
	if len(local) != 1 || len(reflexive) != 3 {
		t.Fatal("merge must not mutate input")
	}
}
