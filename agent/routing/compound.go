package routing

import (
	"fmt"
	"net/netip"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// CloneConfig preserves nil versus empty lists and prevents caller mutations
// from changing an already published policy.
func CloneConfig(cfg Config) Config {
	cfg.Rules = slices.Clone(cfg.Rules)
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		r.Processes = slices.Clone(r.Processes)
		r.Targets = slices.Clone(r.Targets)
		r.Ports = slices.Clone(r.Ports)
		r.Protocols = slices.Clone(r.Protocols)
	}
	return cfg
}

type compoundRule struct {
	networks     []netip.Prefix
	hosts        []string
	addressGlobs []string
	ports        []portRange
}

func targetPrefix(value string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(value); err == nil {
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	if addr, err := netip.ParseAddr(value); err == nil && addr.Zone() == "" {
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), true
	}
	return netip.Prefix{}, false
}

func compileCompound(r Rule) compoundRule {
	var c compoundRule
	for _, target := range r.Targets {
		if prefix, ok := targetPrefix(target); ok {
			c.networks = append(c.networks, prefix)
		} else {
			c.hosts = append(c.hosts, strings.ToLower(strings.TrimSuffix(target, ".")))
			if strings.ContainsAny(target, "*?[") {
				c.addressGlobs = append(c.addressGlobs, strings.ToLower(target))
			}
		}
	}
	for _, ports := range r.Ports {
		if ports == "*" {
			c.ports = append(c.ports, portRange{start: 1, end: 65535})
		} else {
			c.ports = append(c.ports, parsePortRange(ports))
		}
	}
	return c
}

func matchProcess(pattern, process string) bool {
	if pattern == "*" {
		return true
	}
	if process == "" {
		return false
	}
	pat := strings.ReplaceAll(pattern, "\\", "/")
	proc := strings.ReplaceAll(process, "\\", "/")
	if runtime.GOOS == "windows" || strings.ContainsAny(pattern+process, "\\:") {
		pat, proc = strings.ToLower(pat), strings.ToLower(proc)
	}
	if !strings.Contains(pat, "/") {
		proc = path.Base(proc)
	}
	matched, _ := path.Match(pat, proc)
	return matched
}

func (c compoundRule) matches(r Rule, f Flow) bool {
	if len(r.Processes) > 0 && !slices.ContainsFunc(r.Processes, func(p string) bool { return matchProcess(p, f.Process) }) {
		return false
	}
	if len(r.Protocols) > 0 && !slices.ContainsFunc(r.Protocols, func(p string) bool { return p == "*" || strings.EqualFold(p, f.Protocol) }) {
		return false
	}
	if len(c.ports) > 0 && !slices.ContainsFunc(c.ports, func(p portRange) bool { return f.Port >= p.start && f.Port <= max(p.start, p.end) }) {
		return false
	}
	if len(r.Targets) == 0 {
		return true
	}
	addr, _ := netip.ParseAddr(f.IP)
	if !addr.IsValid() {
		addr, _ = netip.ParseAddr(f.Host)
	}
	if addr.IsValid() {
		addr = addr.Unmap().WithZone("")
		for _, prefix := range c.networks {
			if prefix.Contains(addr) {
				return true
			}
		}
		if len(c.addressGlobs) > 0 {
			// Match IP globs against the observed, canonical address. A domain
			// request alone does not supply an IP and must not trigger DNS here.
			ip := addr.String()
			for _, pattern := range c.addressGlobs {
				if matched, _ := path.Match(pattern, ip); matched {
					return true
				}
			}
		}
	}
	host := strings.ToLower(strings.TrimSuffix(f.Host, "."))
	if _, err := netip.ParseAddr(host); err == nil {
		host = ""
	}
	for _, pattern := range c.hosts {
		if pattern == "*" {
			return true
		}
		if host == "" {
			continue
		}
		if strings.HasPrefix(pattern, "*.") {
			// The optional subdomain also applies when the suffix contains
			// wildcards: *.example.* includes example.com and api.example.net.
			if matched, _ := path.Match(pattern[2:], host); matched {
				return true
			}
		}
		if strings.HasPrefix(pattern, ".") && matchDomainSuffix(host, pattern) {
			return true
		}
		if matched, _ := path.Match(pattern, host); matched {
			return true
		}
	}
	return false
}

func validateCompound(rule Rule) error {
	for _, p := range rule.Processes {
		if strings.TrimSpace(p) != p || p == "" || strings.ContainsAny(p, "\x00\r\n") {
			return fmt.Errorf("process condition must be nonempty without surrounding whitespace")
		}
		if _, err := path.Match(strings.ReplaceAll(p, "\\", "/"), ""); err != nil {
			return fmt.Errorf("invalid process glob %q", p)
		}
	}
	for _, p := range rule.Targets {
		if _, ok := targetPrefix(p); ok {
			continue
		}
		if p == "" || strings.ContainsAny(p, " /\\\t\r\n,;\x00") || strings.Trim(p, ".") == "" {
			return fmt.Errorf("invalid domain, IP or CIDR %q", p)
		}
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("invalid domain or IP glob %q", p)
		}
		if strings.Contains(p, ":") {
			// Colons are permitted only in IPv6 globs, not host:port values.
			if !strings.ContainsAny(p, "*?[") || strings.IndexFunc(p, func(r rune) bool {
				return !strings.ContainsRune("0123456789abcdefABCDEF:.*?[]^-", r)
			}) >= 0 {
				return fmt.Errorf("invalid IPv6 glob %q", p)
			}
		}
		// A malformed numeric IP must not silently become a hostname rule.
		if strings.IndexFunc(p, func(r rune) bool { return (r < '0' || r > '9') && r != '.' }) < 0 {
			return fmt.Errorf("invalid IP %q", p)
		}
	}
	for _, p := range rule.Ports {
		if p == "*" {
			continue
		}
		parts := strings.Split(p, "-")
		if len(parts) > 2 {
			return fmt.Errorf("invalid port range %q", p)
		}
		previous := 0
		for _, part := range parts {
			if strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return fmt.Errorf("invalid port range %q", p)
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > 65535 || n < previous {
				return fmt.Errorf("invalid port range %q", p)
			}
			previous = n
		}
	}
	for _, p := range rule.Protocols {
		if p != "*" && !strings.EqualFold(p, "tcp") && !strings.EqualFold(p, "udp") {
			return fmt.Errorf("invalid protocol %q", p)
		}
	}
	return nil
}
