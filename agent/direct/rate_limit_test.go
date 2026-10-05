package direct

import (
	"net"
	"testing"
	"time"
)

func TestHandshakeLimiterBoundsAttemptsPerSource(t *testing.T) {
	limiter := newHandshakeLimiter(2)
	now := time.Unix(1_700_000_000, 0)
	first := &net.UDPAddr{IP: net.ParseIP("203.0.113.10"), Port: 1000}
	second := &net.UDPAddr{IP: net.ParseIP("203.0.113.11"), Port: 1001}

	if !limiter.Allow(first, now) || !limiter.Allow(first, now) {
		t.Fatal("allowed attempts were rejected")
	}
	if limiter.Allow(first, now) {
		t.Fatal("third attempt from the same source was allowed")
	}
	if !limiter.Allow(second, now) {
		t.Fatal("independent source was incorrectly rate limited")
	}
	if !limiter.Allow(first, now.Add(time.Minute)) {
		t.Fatal("source did not recover after the rate window")
	}
}

func TestDirectRemoteKeyIgnoresSourcePort(t *testing.T) {
	first := directRemoteKey(&net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1000})
	second := directRemoteKey(&net.UDPAddr{IP: net.ParseIP("2001:db8::1"), Port: 2000})
	if first == "" || first != second {
		t.Fatalf("remote keys differ: %q %q", first, second)
	}
}
