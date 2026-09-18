package exit

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestInterleaveIPFamiliesPreservesPreferredFamily(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("2001:db8::1"),
		net.ParseIP("2001:db8::2"),
		net.ParseIP("192.0.2.1"),
		net.ParseIP("192.0.2.2"),
	}
	got := interleaveIPFamilies(ips)
	want := []string{"2001:db8::1", "192.0.2.1", "2001:db8::2", "192.0.2.2"}
	if len(got) != len(want) {
		t.Fatalf("got %d addresses, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("address %d=%s want %s", i, got[i], want[i])
		}
	}
}

func TestDNSCacheHitNormalizesHost(t *testing.T) {
	cache := newDNSCache(time.Minute, 4)
	cache.entries["example.test"] = dnsCacheEntry{
		ips:       []net.IP{net.ParseIP("192.0.2.10")},
		expiresAt: time.Now().Add(time.Minute),
	}
	first, err := cache.lookup(context.Background(), "EXAMPLE.TEST.")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].String() != "192.0.2.10" {
		t.Fatalf("unexpected cached result: %v", first)
	}
}

func BenchmarkDNSCacheHit(b *testing.B) {
	cache := newDNSCache(time.Minute, 4)
	cache.entries["example.test"] = dnsCacheEntry{
		ips:       []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("2001:db8::10")},
		expiresAt: time.Now().Add(time.Hour),
	}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := cache.lookup(ctx, "example.test"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInterleaveIPFamilies(b *testing.B) {
	ips := []net.IP{
		net.ParseIP("2001:db8::1"), net.ParseIP("192.0.2.1"),
		net.ParseIP("2001:db8::2"), net.ParseIP("192.0.2.2"),
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = interleaveIPFamilies(ips)
	}
}
