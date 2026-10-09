package routing

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ResolveProxyTarget selects the address presented to an upstream relay or
// custom proxy. DNSModeProxy never runs the Agent's resolver for a known name;
// DNSModeLocal resolves it on the Agent and sends a literal IP. This setting
// applies only when the connection request still carries a hostname. In
// transparent mode the operating system may already have resolved the name.
func (d *RoutingDialer) ResolveProxyTarget(ctx context.Context, host string) (string, error) {
	if d == nil {
		return "", fmt.Errorf("routing: missing dialer for DNS mode")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("routing: empty target")
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return ip.Unmap().String(), nil
	}
	mode := DNSModeProxy
	if d.engine != nil {
		mode = d.engine.Config().DNSMode
	}
	if mode == "" || mode == DNSModeProxy {
		return host, nil
	}
	if mode != DNSModeLocal {
		return "", fmt.Errorf("routing: unsupported DNS mode %q", mode)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("local DNS for %q failed: %w", host, err)
	}
	// A DNS resolution failure must never cause an implicit switch back to
	// proxy-side name resolution (which would change configured behavior).
	for _, ip := range ips {
		if ip.IsValid() && ip.Is4() {
			return ip.Unmap().String(), nil
		}
	}
	for _, ip := range ips {
		if ip.IsValid() {
			return ip.Unmap().String(), nil
		}
	}
	return "", fmt.Errorf("local DNS for %q returned no IP addresses", host)
}
