package exit

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDNSNegativeCacheExpiresAndSharesEntryLimit(t *testing.T) {
	c := newDNSCache(time.Minute, 2)
	calls := 0
	c.lookupIP = func(_ context.Context, _, host string) ([]net.IP, error) {
		calls++
		return nil, &net.DNSError{Name: host, IsNotFound: true, Err: "no such host"}
	}
	for range 3 {
		_, err := c.lookup(context.Background(), "Missing.Example.")
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			t.Fatalf("NXDOMAIN result was lost: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("negative cache made %d queries, want 1", calls)
	}
	c.mu.Lock()
	entry := c.entries["missing.example"]
	if remaining := time.Until(entry.expiresAt); remaining <= 0 || remaining > negativeDNSCacheTTL {
		t.Fatalf("negative TTL=%v", remaining)
	}
	entry.expiresAt = time.Now().Add(-time.Second)
	c.entries["missing.example"] = entry
	c.mu.Unlock()
	_, _ = c.lookup(context.Background(), "missing.example")
	if calls != 2 {
		t.Fatal("expired NXDOMAIN was not retried")
	}
	_, _ = c.lookup(context.Background(), "other.example")
	_, _ = c.lookup(context.Background(), "third.example")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.entries) > 2 {
		t.Fatalf("negative results exceeded the cache budget: %d", len(c.entries))
	}
}

func TestDNSNegativeCacheDoesNotRetainTransientErrors(t *testing.T) {
	for _, failure := range []error{
		context.Canceled, context.DeadlineExceeded,
		&net.DNSError{Err: "temporary", IsTemporary: true},
		&net.DNSError{Err: "timeout", IsTimeout: true},
	} {
		t.Run(failure.Error(), func(t *testing.T) {
			c := newDNSCache(time.Minute, 2)
			calls := 0
			c.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
				calls++
				if calls == 1 {
					return nil, failure
				}
				return []net.IP{net.ParseIP("192.0.2.10")}, nil
			}
			_, _ = c.lookup(context.Background(), "retry.example")
			ips, err := c.lookup(context.Background(), "retry.example")
			if err != nil || len(ips) != 1 || calls != 2 {
				t.Fatalf("transient DNS failure was cached: ips=%v err=%v calls=%d", ips, err, calls)
			}
		})
	}
}
