package routing

import (
	"fmt"
	"net/netip"
	"strings"
)

// ValidateConfig checks disabled rules too, so enabling one cannot widen a
// malformed selector into an accidental catch-all.
func ValidateConfig(cfg Config) error {
	switch cfg.Mode {
	case "", ModeRule, ModeGlobalProxy, ModeDirect:
	default:
		return fmt.Errorf("routing.mode: invalid value %q", cfg.Mode)
	}
	if cfg.DNSMode != "" && cfg.DNSMode != DNSModeLocal && cfg.DNSMode != DNSModeProxy {
		return fmt.Errorf("routing.dns_mode: invalid value %q (expected proxy or local)", cfg.DNSMode)
	}
	if cfg.ProxyDNSEnabled && (cfg.DNSMode == DNSModeLocal || cfg.AutoDetectDNS) {
		return fmt.Errorf("routing.proxy_dns_enabled requires manual routing.dns_mode=proxy (local DNS auto-detection would re-resolve or leak the hostname)")
	}
	if cfg.ProxyDNSEnabled && cfg.FakeIPEnabled {
		return fmt.Errorf("routing.proxy_dns_enabled and routing.fake_ip_enabled are mutually exclusive DNS/53 capture modes")
	}
	if cfg.AutoDetectDNS && cfg.FakeIPEnabled {
		return fmt.Errorf("routing.auto_detect_dns cannot be combined with FakeIP DNS interception")
	}
	if cfg.FakeIPEnabled && cfg.DNSMode == DNSModeLocal {
		return fmt.Errorf("routing.fake_ip_enabled requires routing.dns_mode=proxy")
	}
	if cfg.BlockDoHEndpoints && !cfg.FakeIPEnabled {
		return fmt.Errorf("routing.block_doh_endpoints requires routing.fake_ip_enabled")
	}
	if cfg.ForwardOtherDNS && !cfg.FakeIPEnabled {
		return fmt.Errorf("routing.forward_other_dns requires routing.fake_ip_enabled")
	}
	if cfg.DNSExitID != "" && (strings.TrimSpace(cfg.DNSExitID) != cfg.DNSExitID || strings.ContainsAny(cfg.DNSExitID, " \t\r\n/\\") || len(cfg.DNSExitID) > 128) {
		return fmt.Errorf("routing.dns_exit_id: invalid proxy exit identifier")
	}
	if err := ValidateDNSUpstreams(cfg.DNSUpstreams); err != nil {
		return err
	}
	if len(cfg.DoHBlockedIPs) > 256 {
		return fmt.Errorf("routing.doh_blocked_ips: maximum 256 IP/CIDR entries")
	}
	for index, value := range cfg.DoHBlockedIPs {
		raw := strings.TrimSpace(value)
		if raw == "" || raw != value {
			return fmt.Errorf("routing.doh_blocked_ips[%d]: empty or whitespace address", index)
		}
		var prefix netip.Prefix
		if address, err := netip.ParseAddr(raw); err == nil {
			if address.Zone() != "" || !address.IsGlobalUnicast() || address.IsPrivate() {
				return fmt.Errorf("routing.doh_blocked_ips[%d]: address must be public and zone-free", index)
			}
			prefix = netip.PrefixFrom(address.Unmap(), address.Unmap().BitLen())
		} else {
			parsed, parseErr := netip.ParsePrefix(raw)
			if parseErr != nil || parsed.Addr().Zone() != "" || !parsed.IsValid() ||
				!parsed.Addr().IsGlobalUnicast() || parsed.Addr().IsPrivate() {
				return fmt.Errorf("routing.doh_blocked_ips[%d]: invalid public IP/CIDR %q", index, value)
			}
			prefix = parsed.Masked()
		}
		// Explicitly target limited public resolver addresses. Large CIDRs
		// cover unrelated CDN / browser traffic; reject unsafe broad blocks.
		if (prefix.Addr().Is4() && prefix.Bits() < 24) || (prefix.Addr().Is6() && prefix.Bits() < 48) {
			return fmt.Errorf("routing.doh_blocked_ips[%d]: prefix too broad; use IPv4 /24 or IPv6 /48 or narrower", index)
		}
	}
	validAction := func(a Action) bool { return a == ActionProxy || a == ActionDirect || a == ActionReject }
	if cfg.DefaultAction != "" && !validAction(cfg.DefaultAction) {
		return fmt.Errorf("routing.default_action: invalid value %q", cfg.DefaultAction)
	}
	if len(cfg.Subscriptions) > subscriptionLimit {
		return fmt.Errorf("routing.subscriptions: maximum %d subscriptions", subscriptionLimit)
	}
	seenURLs := make(map[string]bool, len(cfg.Subscriptions))
	for i, sub := range cfg.Subscriptions {
		if err := validateSubscriptionURL(sub.URL); err != nil {
			return fmt.Errorf("routing.subscriptions[%d]: %w", i, err)
		}
		if seenURLs[sub.URL] {
			return fmt.Errorf("routing.subscriptions[%d]: duplicate URL", i)
		}
		seenURLs[sub.URL] = true
		if len(sub.Name) > 128 || strings.TrimSpace(sub.Name) == "" {
			return fmt.Errorf("routing.subscriptions[%d]: name required (max 128)", i)
		}
		if sub.Action != "" && !validAction(sub.Action) {
			return fmt.Errorf("routing.subscriptions[%d]: invalid action %q", i, sub.Action)
		}
		if sub.ExitID != "" && (sub.Action != "" && sub.Action != ActionProxy || strings.TrimSpace(sub.ExitID) != sub.ExitID || strings.ContainsAny(sub.ExitID, " \t\r\n/\\") || len(sub.ExitID) > 128) {
			return fmt.Errorf("routing.subscriptions[%d]: invalid proxy exit ID", i)
		}
	}
	for i, rule := range cfg.Rules {
		if !validAction(rule.Action) {
			return fmt.Errorf("routing.rules[%d] (%q): invalid action %q", i, rule.Name, rule.Action)
		}
		if rule.HandleDirect && rule.Action != ActionDirect {
			return fmt.Errorf("routing.rules[%d] (%q): handle_direct is only valid for DIRECT", i, rule.Name)
		}
		if rule.HandleDirect && rule.DatagramRequired {
			return fmt.Errorf("routing.rules[%d] (%q): handle_direct cannot require relay datagrams", i, rule.Name)
		}
		if err := validateCompound(rule); err != nil {
			return fmt.Errorf("routing.rules[%d] (%q): %w", i, rule.Name, err)
		}
	}
	return nil
}
