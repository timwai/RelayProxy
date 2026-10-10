package divert

import (
	"fmt"
	"net/netip"
)

// These destinations are not suitable for a public DIRECT socket recovered
// from a FakeIP. Local/private destinations need explicit split-DNS policy,
// never an unauthenticated public resolver response (DNS rebinding).
var fakeDirectReserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001::/32"), // tunneling/transition space
}

func safeFakeDirectAddress(raw string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("DIRECT DNS returned invalid IP %q: %w", raw, err)
	}
	addr = addr.Unmap()
	if addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsPrivate() ||
		addr.IsLoopback() || addr.IsLinkLocalUnicast() || isFakeIP(addr) {
		return netip.Addr{}, fmt.Errorf("DIRECT DNS returned non-public IP %s", addr)
	}
	for _, excluded := range fakeDirectReserved {
		if excluded.Contains(addr) {
			return netip.Addr{}, fmt.Errorf("DIRECT DNS returned reserved IP %s", addr)
		}
	}
	return addr, nil
}

// A hostname rule can allow DIRECT while a higher-priority CIDR rule blocks
// the real destination. Reevaluate with both host and resolved IP, preserving
// process identity, and fail closed if the policy now requires PROXY/REJECT.
// This also rejects relay self-loops and loopback listeners.
func (s *Server) validateFakeDirectTarget(route *ClassifiedFlow, raw string) error {
	addr, err := safeFakeDirectAddress(raw)
	if err != nil {
		return err
	}
	if route == nil || route.owner != s || route.flow.DomainSource != "fakeip" ||
		route.decision.Action != ActionDirect {
		return fmt.Errorf("DIRECT FakeIP target has no authorized route")
	}
	if addr.Is6() != route.key.Destination.Addr().Is6() {
		return fmt.Errorf("DIRECT DNS address family mismatch for %s", addr)
	}
	if s.opts.PolicyMu != nil {
		s.opts.PolicyMu.RLock()
		defer s.opts.PolicyMu.RUnlock()
	}
	if !s.fakeIPEnabled() {
		return fmt.Errorf("FakeIP DNS protection disabled during DIRECT resolution")
	}
	flow := route.flow
	flow.IP = addr.String()
	if s.guard.MustDirectFlow(flow) {
		// The proxy control endpoint and local listeners must never be
		// accessed through an arbitrary DNS alias (rebinding or a loop).
		return fmt.Errorf("DIRECT destination conflicts with local/relay loop guard: %s", addr)
	}
	decision := s.engine.MatchWith(flow, s.opts.SharedPolicy)
	if decision.Action != ActionDirect {
		return fmt.Errorf("DIRECT target %s denied by updated routing policy (%s: %s)", addr, decision.Rule, decision.Action)
	}
	return nil
}
