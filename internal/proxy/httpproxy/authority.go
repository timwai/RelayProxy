package httpproxy

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
)

// parseAuthority validates before routing/ACL evaluation, and canonicalizes IP
// literals so empty hosts and malformed authorities cannot reach the dialer.
func parseAuthority(authority string, defaultPort uint16) (string, uint16, error) {
	invalid := errors.New("invalid HTTP proxy authority")
	host := authority
	port := defaultPort
	if h, p, err := net.SplitHostPort(authority); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return "", 0, invalid
		}
		host, port = h, uint16(n)
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() {
			return "", 0, invalid
		}
	}
	if host == "" {
		return "", 0, invalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" {
			return "", 0, invalid
		}
		return ip.Unmap().String(), port, nil
	}
	// DNS lookup accepts IDN via ASCII labels. Preserve the conventional
	// trailing dot and permit ASCII underscores used by internal hostnames.
	host = strings.NewReplacer("。", ".", "．", ".", "｡", ".").Replace(host)
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	for i, label := range labels {
		if strings.IndexFunc(label, func(r rune) bool { return r > 127 }) >= 0 {
			ascii, err := idna.Lookup.ToASCII(label)
			if err != nil {
				return "", 0, invalid
			}
			labels[i] = ascii
		}
	}
	name := strings.Join(labels, ".")
	if len(name) == 0 || len(name) > 253 {
		return "", 0, invalid
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", 0, invalid
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return "", 0, invalid
			}
		}
	}
	if strings.HasSuffix(host, ".") {
		name += "."
	}
	return strings.ToLower(name), port, nil
}
