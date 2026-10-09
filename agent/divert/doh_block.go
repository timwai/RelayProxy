package divert

import "strings"

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
