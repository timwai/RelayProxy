package routing

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// DNSUpstream is an authenticated DoH endpoint reached via the configured
// proxy DNS exit. BootstrapIP is used only as the TCP dial target; URL's
// hostname remains the TLS certificate identity and HTTP authority.
type DNSUpstream struct {
	URL         string `yaml:"url" json:"url"`
	BootstrapIP string `yaml:"bootstrap_ip" json:"bootstrap_ip"`
}

// ValidateDNSUpstreams accepts explicit, HTTPS-only DNS services. Requiring a
// literal dial IP prevents recursive bootstrap via the Agent system resolver.
// A private bootstrap address is permitted deliberately for an exit-local
// DoH server; the proxy exit's own ACL must authorize that destination.
func ValidateDNSUpstreams(upstreams []DNSUpstream) error {
	if len(upstreams) > 8 {
		return fmt.Errorf("routing.dns_upstreams: maximum 8 resolvers")
	}
	for i, upstream := range upstreams {
		prefix := fmt.Sprintf("routing.dns_upstreams[%d]", i)
		if strings.TrimSpace(upstream.URL) != upstream.URL || len(upstream.URL) > 512 {
			return fmt.Errorf("%s.url: invalid whitespace or length", prefix)
		}
		parsed, err := url.Parse(upstream.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			parsed.Hostname() == "" || (parsed.Port() != "" && parsed.Port() != "443") ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
			parsed.Opaque != "" || parsed.Path == "" {
			return fmt.Errorf("%s.url: expected https://hostname/dns-query on port 443 without credentials, query or fragment", prefix)
		}
		host := parsed.Hostname()
		if netipAddr, err := netip.ParseAddr(host); err == nil && netipAddr.IsValid() {
			return fmt.Errorf("%s.url: TLS hostname is required rather than a bare IP", prefix)
		}
		if len(host) > 253 || !strings.Contains(host, ".") || strings.ContainsAny(host, " /\\\t\r\n") {
			return fmt.Errorf("%s.url: invalid DoH TLS hostname", prefix)
		}
		for _, label := range strings.Split(host, ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return fmt.Errorf("%s.url: invalid hostname label", prefix)
			}
			for _, ch := range label {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
					(ch >= '0' && ch <= '9') || ch == '-') {
					return fmt.Errorf("%s.url: hostname must be ASCII DNS", prefix)
				}
			}
		}
		ip, err := netip.ParseAddr(upstream.BootstrapIP)
		if err != nil || !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsLoopback() ||
			ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() || ip.Is4In6() ||
			strings.TrimSpace(upstream.BootstrapIP) != upstream.BootstrapIP {
			return fmt.Errorf("%s.bootstrap_ip: expected a non-loopback IPv4/IPv6 address", prefix)
		}
	}
	return nil
}
