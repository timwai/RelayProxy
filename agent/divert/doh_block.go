package divert

import (
	"net/netip"
	"strings"
)

// Known public DoH resolver hostnames. This is deliberately NOT a generic
// HTTPS/443 blocklist: anycast/CDN IPs often host unrelated services.
// This best-effort guard applies only to trusted pre-connect hostnames
// (FakeIP / validated DNS answer / Network Extension remoteHostname).
//
// Applications connecting by fixed IP or using ECH/obfuscated/private
// endpoints will not be identified. Strict DNS privacy therefore still
// requires OS-level DNS and VPN enforcement.
var knownDoHEndpoints = map[string]struct{}{
	"dns.google":                  {},
	"dns.google.com":              {},
	"cloudflare-dns.com":          {},
	"mozilla.cloudflare-dns.com":  {},
	"security.cloudflare-dns.com": {},
	"family.cloudflare-dns.com":   {},
	"dns.quad9.net":               {},
	"dns10.quad9.net":             {},
	"dns11.quad9.net":             {},
	"doh.opendns.com":             {},
	"dns.nextdns.io":              {},
	"dns.adguard-dns.com":         {},
}

func isKnownDoHEndpoint(raw string) bool {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	_, matched := knownDoHEndpoints[name]
	return matched
}

// matchesConfiguredDoHIP blocks ONLY explicitly supplied IPv4/IPv6 endpoints
// on TCP/UDP 443. It never guesses endpoint addresses from shared CDN IPs,
// never blocks port 443 globally, and never uses untrusted SNI as evidence.
// The routing config validator restricts CIDRs to narrow public networks.
func matchesConfiguredDoHIP(raw string, entries []string) bool {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, entry := range entries {
		if ip, err := netip.ParseAddr(entry); err == nil {
			if ip.Unmap() == addr {
				return true
			}
			continue
		}
		prefix, err := netip.ParsePrefix(entry)
		if err == nil && prefix.Contains(addr) {
			return true
		}
	}
	return false
}
