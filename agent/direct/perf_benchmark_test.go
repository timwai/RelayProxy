package direct

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"relayproxy/internal/acl"
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
			Protocol:        protocol.PublicDirectEndpointProtocolUDP,
			Address:         fmt.Sprintf("exit-%02d.example.com:35820", i),
			Source:          source,
			Verified:        true,
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

func BenchmarkTicketAuthenticatorAuthenticatePolicy(b *testing.B) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	auth, err := NewTicketAuthenticator("benchmark-server", publicKey, "exit")
	if err != nil {
		b.Fatal(err)
	}
	auth.now = func() time.Time { return now }

	checker, err := acl.NewChecker(acl.Policy{
		ID: "benchmark-relay", AllowInternet: true,
		AccessMode: acl.AccessModeDeny, AccessHosts: []string{"blocked.example"},
	})
	if err != nil {
		b.Fatal(err)
	}
	policy := checker.Policy()
	auth.SetCurrentValidator(func(context.Context, protocol.PublicDirectTicketClaims) (Authorization, error) {
		return Authorization{RelayPolicy: &policy}, nil
	})

	tickets := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		nonce := make([]byte, 32)
		binary.LittleEndian.PutUint64(nonce, uint64(i+1))
		claims := protocol.PublicDirectTicketClaims{
			Version:               protocol.PublicDirectTicketVersion,
			Issuer:                "benchmark-server",
			ClientDeviceID:        "client",
			ExitDeviceID:          "exit",
			IssuedAt:              now.Unix(),
			ExpiresAt:             now.Add(5 * time.Minute).Unix(),
			PolicyRevision:        4,
			AuthorizationRevision: 9,
			Nonce:                 nonce,
			AllowedCapabilities:   []string{protocol.PublicDirectTicketCapabilityProxy},
		}
		signed := protocol.PublicDirectSignedTicket{
			Claims:    claims,
			Signature: ed25519.Sign(privateKey, protocol.PublicDirectTicketPayload(claims)),
		}
		raw, err := json.Marshal(signed)
		if err != nil {
			b.Fatal(err)
		}
		tickets[i] = raw
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := auth.AuthenticatePolicy(context.Background(), protocol.PublicDirectAuthRequest{
			Version:        protocol.PublicDirectAuthVersion,
			ClientDeviceID: "client",
			ExitDeviceID:   "exit",
			Ticket:         tickets[i],
		}); err != nil {
			b.Fatal(err)
		}
	}
}
