package gateway

import (
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func TestProxyStreamResumeNegotiationRequiresBothPeers(t *testing.T) {
	client := &session.DeviceSession{Capabilities: []string{protocol.CapabilityProxyStreamResume}}
	exit := &session.DeviceSession{Capabilities: []string{protocol.CapabilityProxyStreamResume}}
	if !proxyStreamResumeNegotiated(client, exit) {
		t.Fatal("matching client/exit capability was not negotiated")
	}
	exit.Capabilities = nil
	if proxyStreamResumeNegotiated(client, exit) {
		t.Fatal("client-only capability unexpectedly enabled stream resume")
	}
	exit.Capabilities = []string{protocol.CapabilityProxyStreamResume}
	client.Capabilities = nil
	if proxyStreamResumeNegotiated(client, exit) {
		t.Fatal("exit-only capability unexpectedly enabled stream resume")
	}
}

func TestValidateTCPResumeBinding(t *testing.T) {
	valid := &protocol.TCPResumeBinding{
		Mode:       protocol.TCPResumeModeRebind,
		StreamID:   make([]byte, protocol.TCPResumeStreamIDSize),
		Token:      make([]byte, protocol.TCPResumeTokenSize),
		Generation: 2,
	}
	if err := validateTCPResumeBinding(valid); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*protocol.TCPResumeBinding)
	}{
		{"mode", func(v *protocol.TCPResumeBinding) { v.Mode = "unknown" }},
		{"stream id", func(v *protocol.TCPResumeBinding) { v.StreamID = v.StreamID[:1] }},
		{"token", func(v *protocol.TCPResumeBinding) { v.Token = v.Token[:1] }},
		{"generation", func(v *protocol.TCPResumeBinding) { v.Generation = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *valid
			copy.StreamID = append([]byte(nil), valid.StreamID...)
			copy.Token = append([]byte(nil), valid.Token...)
			tc.edit(&copy)
			if err := validateTCPResumeBinding(&copy); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}
