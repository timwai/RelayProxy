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
	cfg := Config{DNSMode: DNSModeProxy}
	if d.engine != nil {
		cfg = d.engine.Config()
	}
	// Explicit proxy-side resolution must take precedence over automatic
	// DNS detection. A domain that the user chose to proxy must not be
	// silently converted to a locally resolved IP (or disclosed via DNS).
	if cfg.DNSMode == "" || cfg.DNSMode == DNSModeProxy {
		return host, nil
	}
	if cfg.DNSMode != DNSModeLocal {
		return "", fmt.Errorf("routing: unsupported DNS mode %q", cfg.DNSMode)
	}
	if cfg.AutoDetectDNS {
		// Automatic fallback applies only to local-resolution mode: first
		// try the system resolver, then send the hostname to the proxy if
		// it fails. Never fall back after cancellation or under FakeIP.
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		return autoDNSResolution(ctx, host, ips, err)
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("local DNS for %q failed: %w", host, err)
	}
	// Manual local DNS must not silently fall back to the proxy.
	if target := firstResolvedIP(ips); target != "" {
		return target, nil
	}
	return "", fmt.Errorf("local DNS for %q returned no IP addresses", host)
}

// Prefer IPv4 for the system resolver path, preserving the existing
// local-resolution policy and leaving the DNS choice deterministic.
func firstResolvedIP(ips []netip.Addr) string {
	for _, ip := range ips {
		if ip.IsValid() && ip.Is4() {
			return ip.Unmap().String()
		}
	}
	for _, ip := range ips {
		if ip.IsValid() {
			return ip.Unmap().String()
		}
	}
	return ""
}

// autoDNSResolution lets the automatic resolver's error/cancellation policy
// be tested without relying on external DNS or machine-specific search lists.
func autoDNSResolution(ctx context.Context, host string, ips []netip.Addr, lookupErr error) (string, error) {
	if err := ctx.Err(); err != nil {
		// Never send an unresolved hostname upstream after cancellation.
		return "", err
	}
	if lookupErr == nil {
		if target := firstResolvedIP(ips); target != "" {
			return target, nil
		}
	}
	// DNS failure or an empty response: the remote proxy may still be able
	// to resolve the original domain. Avoid retrying the local resolver.
	return host, nil
}
