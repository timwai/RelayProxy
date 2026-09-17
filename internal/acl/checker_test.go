package acl

import (
	"context"
	"net"
	"testing"
)

func TestACLChecker(t *testing.T) {
	policy := Policy{
		ID:                  "p1",
		Name:                "Test Policy",
		AllowInternet:       true,
		AllowPrivateNetwork: false,
		Rules: []Rule{
			{
				Priority:    10,
				Action:      ActionAllow,
				TargetType:  TargetCIDR,
				TargetValue: "10.20.0.0/16",
				PortStart:   80,
				PortEnd:     443,
			},
			{
				Priority:    20,
				Action:      ActionDeny,
				TargetType:  TargetDomainSuffix,
				TargetValue: "malicious.com",
			},
		},
	}

	checker, err := NewChecker(policy)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	ctx := context.Background()

	// Dangerous loopback should fail
	if err := checker.CheckIP(ctx, net.ParseIP("127.0.0.1"), 80); err == nil {
		t.Errorf("expected 127.0.0.1 to be blocked")
	}

	// Normal internet IP should pass
	if err := checker.CheckIP(ctx, net.ParseIP("8.8.8.8"), 443); err != nil {
		t.Errorf("expected 8.8.8.8 to pass, got: %v", err)
	}

	// Unallowed private IP should fail
	if err := checker.CheckIP(ctx, net.ParseIP("192.168.1.1"), 80); err == nil {
		t.Errorf("expected 192.168.1.1 to be blocked by private network rule")
	}

	// Blocked domain suffix should fail
	if err := checker.CheckHost(ctx, "evil.malicious.com", 80); err == nil {
		t.Errorf("expected evil.malicious.com to be blocked")
	}

	// Allowed domain suffix should pass
	if err := checker.CheckHost(ctx, "good.google.com", 443); err != nil {
		t.Errorf("expected good.google.com to pass, got: %v", err)
	}
}

func TestACLRulePriority(t *testing.T) {
	// Rule with lower priority appears first, but higher priority rule should evaluate first
	policy := Policy{
		ID:            "p2",
		AllowInternet: true,
		Rules: []Rule{
			{
				Priority:    5,
				Action:      ActionAllow,
				TargetType:  TargetDomain,
				TargetValue: "blocked.com",
			},
			{
				Priority:    100,
				Action:      ActionDeny,
				TargetType:  TargetDomain,
				TargetValue: "blocked.com",
			},
		},
	}

	checker, err := NewChecker(policy)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	ctx := context.Background()
	if err := checker.CheckHost(ctx, "blocked.com", 80); err == nil {
		t.Errorf("expected higher priority DENY rule to win over lower priority ALLOW rule")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern string
		s       string
		want    bool
	}{
		{"*", "anything", true},
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", true},
		{"api.*.com", "api.v1.com", true},
		{"api.*.com", "api.com", false},
		{"a?c.com", "abc.com", true},
		{"a?c.com", "ac.com", false},
		{"exact.com", "exact.com", true},
		{"exact.com", "sub.exact.com", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.s); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestAccessAllowlistDomains(t *testing.T) {
	checker, err := NewChecker(Policy{
		AllowInternet: true,
		AccessMode:    AccessModeAllow,
		AccessHosts:   []string{"*.example.com", ".internal.org", "exact.io"},
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	ctx := context.Background()

	for _, host := range []string{"api.example.com", "a.b.example.com", "internal.org", "svc.internal.org", "exact.io"} {
		if err := checker.CheckHost(ctx, host, 443); err != nil {
			t.Errorf("expected %s to be allowed, got %v", host, err)
		}
	}
	for _, host := range []string{"example.com", "evil.com", "notinternal.org", "exact.io.evil.com"} {
		if err := checker.CheckHost(ctx, host, 443); err == nil {
			t.Errorf("expected %s to be denied by allow-list", host)
		}
	}
}

func TestAccessAllowlistCIDRAndRange(t *testing.T) {
	checker, err := NewChecker(Policy{
		AllowInternet:       true,
		AllowPrivateNetwork: true,
		AccessMode:          AccessModeAllow,
		AccessCIDRs:         []string{"10.0.0.0/8", "1.2.3.4", "192.168.1.10-192.168.1.20"},
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	ctx := context.Background()

	for _, host := range []string{"10.1.2.3", "1.2.3.4", "192.168.1.10", "192.168.1.15", "192.168.1.20"} {
		if err := checker.CheckHost(ctx, host, 443); err != nil {
			t.Errorf("expected %s to be allowed, got %v", host, err)
		}
	}
	for _, host := range []string{"8.8.8.8", "172.16.0.1", "192.168.1.9", "192.168.1.21"} {
		if err := checker.CheckHost(ctx, host, 443); err == nil {
			t.Errorf("expected %s to be denied by allow-list", host)
		}
	}
}

func TestAccessDenylist(t *testing.T) {
	checker, err := NewChecker(Policy{
		AllowInternet:       true,
		AllowPrivateNetwork: true,
		AccessMode:          AccessModeDeny,
		AccessHosts:         []string{"*.ads.com", "tracker.io"},
		AccessCIDRs:         []string{"10.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	ctx := context.Background()

	// Domain denylist
	for _, host := range []string{"x.ads.com", "tracker.io"} {
		if err := checker.CheckHost(ctx, host, 443); err == nil {
			t.Errorf("expected %s to be denied by deny-list", host)
		}
	}
	if err := checker.CheckHost(ctx, "good.com", 443); err != nil {
		t.Errorf("expected good.com to pass, got %v", err)
	}

	// CIDR denylist (direct IP and resolved-IP phase)
	if err := checker.CheckHost(ctx, "10.0.0.5", 443); err == nil {
		t.Errorf("expected 10.0.0.5 to be denied")
	}
	if err := checker.CheckIP(ctx, net.ParseIP("10.0.0.7"), 443); err == nil {
		t.Errorf("expected resolved 10.0.0.7 to be denied by deny-list")
	}
	if err := checker.CheckIP(ctx, net.ParseIP("8.8.8.8"), 443); err != nil {
		t.Errorf("expected 8.8.8.8 to pass, got %v", err)
	}
}

func TestAccessInvalidEntries(t *testing.T) {
	if _, err := NewChecker(Policy{AccessCIDRs: []string{"not-an-ip"}}); err == nil {
		t.Errorf("expected invalid CIDR to be rejected")
	}
	if _, err := NewChecker(Policy{AccessCIDRs: []string{"10.0.0.1-abc"}}); err == nil {
		t.Errorf("expected invalid range to be rejected")
	}
	if _, err := NewChecker(Policy{AccessCIDRs: []string{"9.9.9.9-1.1.1.1"}}); err == nil {
		t.Errorf("expected reversed range to be rejected")
	}
}
