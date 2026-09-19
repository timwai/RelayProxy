package exit

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
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

func TestDNSCacheCoalescesConcurrentMisses(t *testing.T) {
	cache := newDNSCache(time.Minute, 4)
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	cache.lookupIP = func(ctx context.Context, network, host string) ([]net.IP, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			return []net.IP{net.ParseIP("192.0.2.42")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const goroutines = 32
	begin := make(chan struct{})
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-begin
			ips, err := cache.lookup(context.Background(), "Burst.Example.")
			if err == nil && (len(ips) != 1 || ips[0].String() != "192.0.2.42") {
				err = fmt.Errorf("unexpected result: %v", ips)
			}
			errs <- err
		}()
	}
	close(begin)
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver called %d times, want 1", got)
	}
}
