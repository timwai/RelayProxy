package systemproxy

import (
	"net/netip"
	"strconv"
	"strings"
)

// filterIPBypass keeps the user's ordinary hostname exceptions and safety
// exceptions while removing rules that would bypass RelayProxy for a literal
// routable IP URL. This never alters application-level proxy behavior.
func filterIPBypass(before []string) []string {
	after := make([]string, 0, len(before))
	for _, pattern := range before {
		if !ipBypassRule(pattern) {
			after = append(after, pattern)
		}
	}
	return after
}

func ipBypassRule(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	// Do not alter special Chromium rules, localhost, hostnames or generic
	// wildcard rules. Removing a global '*' can change unrelated app traffic.
	if strings.Contains(s, "://") || strings.ContainsAny(s, "<>") {
		return false
	}
	if prefix, err := netip.ParsePrefix(strings.Trim(s, "[]")); err == nil {
		ip := prefix.Addr().Unmap()
		return proxyableIP(ip) && prefix.Bits() >= 8
	}
	if ip, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
		return proxyableIP(ip)
	}
	// Some user-defined bypass lists have dotted IPv4 glob patterns.
	// Never reinterpret a hostname containing letters or a global '*'.
	if !strings.Contains(s, "*") || !strings.Contains(s, ".") {
		return false
	}
	fields := strings.Split(s, ".")
	if len(fields) != 4 {
		return false
	}
	fixed := 0
	for _, f := range fields {
		if f == "*" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > 255 {
			return false
		}
		fixed++
	}
	if fixed == 0 {
		return false
	}
	// Keep all loopback and link-local subnets explicitly excluded from the
	// proxy, even where Chromium would otherwise have implicit bypasses.
	if fields[0] == "127" || (fields[0] == "169" && (fields[1] == "254" || fields[1] == "*")) {
		return false
	}
	return true
}

func proxyableIP(addr netip.Addr) bool {
	return addr.IsValid() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() &&
		!addr.IsLinkLocalMulticast() && !addr.IsMulticast() && !addr.IsUnspecified()
}
