package divert

import (
	"context"
	"fmt"
	"net/netip"
)

func (s *Server) resolveRouteTarget(ctx context.Context, route *ClassifiedFlow) (string, error) {
	var target string
	var err error
	if s.opts.ResolveTarget != nil {
		target, err = s.opts.ResolveTarget(ctx, route.flow, route.decision)
	} else {
		target = route.decision.DNSIP
	}
	if err != nil {
		return "", err
	}
	ip, err := netip.ParseAddr(target)
	if err != nil || ip.Zone() != "" {
		return "", fmt.Errorf("matched routing DNS returned no valid IP")
	}
	target = ip.Unmap().String()
	if route.decision.Action == ActionDirect && route.decision.DNSResolver != "static" && route.flow.DomainSource == "fakeip" && isFakeIP(route.key.Destination.Addr()) {
		if err := s.validateFakeDirectTarget(route, target); err != nil {
			return "", err
		}
	}
	if route.traffic != nil {
		route.traffic.SetIP(target)
	}
	return target, nil
}
