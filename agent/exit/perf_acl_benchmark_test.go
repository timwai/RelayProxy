package exit

import (
	"testing"

	"relayproxy/internal/acl"
)

func BenchmarkCompiledRelayACLCacheHit(b *testing.B) {
	base, err := acl.NewChecker(acl.Policy{
		ID: "relay",
		AllowInternet: true,
		AllowPrivateNetwork: true,
		AccessMode: acl.AccessModeDeny,
		AccessHosts: []string{"*.blocked.example"},
	})
	if err != nil { b.Fatal(err) }
	policy := base.Policy()
	h := NewHandler(HandlerConfig{})
	if _, err := h.compiledRelayACL(&policy); err != nil { b.Fatal(err) }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := h.compiledRelayACL(&policy); err != nil { b.Fatal(err) }
	}
}
