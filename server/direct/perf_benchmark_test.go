package direct

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func BenchmarkVerifiedPublicDirectEndpoints(b *testing.B) {
	registry := NewRegistry()
	candidates := make([]protocol.PublicDirectEndpointCandidate, 0, 16)
	for i := 0; i < 16; i++ {
		candidates = append(candidates, protocol.PublicDirectEndpointCandidate{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address: fmt.Sprintf("exit-%02d.example.com:35820", i),
			Source: protocol.PublicDirectEndpointManual,
		})
	}
	records, err := registry.Register("exit", "session", netip.Addr{}, protocol.PublicDirectRegistrationRequest{
		CertFingerprint: testFingerprint(),
		NetworkEpoch: 1,
		Candidates: candidates,
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, record := range records {
		if !registry.MarkVerified("exit", "session", record.Endpoint.Address, time.Hour) {
			b.Fatalf("failed to verify %s", record.Endpoint.Address)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := registry.VerifiedEndpoints("exit"); len(got) != len(records) {
			b.Fatalf("verified=%d want=%d", len(got), len(records))
		}
	}
}
