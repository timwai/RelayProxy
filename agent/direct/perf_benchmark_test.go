package direct

import (
	"fmt"
	"testing"

	"relayproxy/internal/protocol"
)

func BenchmarkSelectPublicDirectEndpoints(b *testing.B) {
	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	path := protocol.ProxyPublicDirectPath{Available: true, Transport: "quic"}
	for i := 0; i < 16; i++ {
		source := protocol.PublicDirectEndpointIPv6
		if i%3 == 0 {
			source = protocol.PublicDirectEndpointManual
		} else if i%3 == 1 {
			source = protocol.PublicDirectEndpointObserved
		}
		path.Endpoints = append(path.Endpoints, protocol.PublicDirectEndpoint{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address: fmt.Sprintf("exit-%02d.example.com:35820", i),
			Source: source,
			Verified: true,
			CertFingerprint: fingerprint,
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := selectPublicEndpoints(path); len(got) != len(path.Endpoints) {
			b.Fatalf("selected=%d want=%d", len(got), len(path.Endpoints))
		}
	}
}
