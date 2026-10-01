package protocol

import (
	"encoding/json"
	"testing"
)

func TestP2PControlMessageRoundTrip(t *testing.T) {
	in := P2PControlMessage{
		Type:            P2PControlConnectRequest,
		ExitDeviceID:    "dev_exit",
		Candidates:      []P2PCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.10:52133", Priority: 100}},
		CertFingerprint: "sha256:test",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out P2PControlMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != in.Type || out.ExitDeviceID != in.ExitDeviceID || out.CertFingerprint != in.CertFingerprint {
		t.Fatalf("round trip mismatch: %#v", out)
	}
	if len(out.Candidates) != 1 || out.Candidates[0] != in.Candidates[0] {
		t.Fatalf("candidate round trip mismatch: %#v", out.Candidates)
	}
}

func TestRDPCandidateAliasUsesGenericP2PShape(t *testing.T) {
	var legacy RDPCandidate = P2PCandidate{
		Protocol: "udp",
		Type:     "reflexive",
		Address:  "198.51.100.4:41001",
		Priority: 10,
	}
	if legacy.Address != "198.51.100.4:41001" {
		t.Fatalf("legacy candidate alias changed: %#v", legacy)
	}
}
