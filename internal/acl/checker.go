package acl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sort"
	"strings"
)

type Action string

const (
	ActionAllow Action = "ALLOW"
	ActionDeny  Action = "DENY"
)

type TargetType string

const (
	TargetAny          TargetType = "ANY"
	TargetCIDR         TargetType = "CIDR"
	TargetDomain       TargetType = "DOMAIN"
	TargetDomainSuffix TargetType = "DOMAIN_SUFFIX"
	TargetGlob         TargetType = "GLOB" // wildcard pattern over the hostname ("*" and "?")
)

// AccessMode selects the semantics of Policy.AccessHosts / AccessCIDRs.
type AccessMode string

const (
	// AccessModeNone means no destination access-list gate: only the boolean
	// flags (AllowInternet / AllowPrivateNetwork / AllowLoopback) and Rules apply.
	AccessModeNone AccessMode = ""
	// AccessModeAllow is a whitelist: a destination is reachable only when it
	// matches a host pattern or CIDR in the access lists.
	AccessModeAllow AccessMode = "allow"
	// AccessModeDeny is a blacklist: a destination matching a host pattern or
	// CIDR is blocked.
	AccessModeDeny AccessMode = "deny"
)

type Rule struct {
	Priority    int
	Action      Action
	Protocol    string     // "tcp", "udp", or empty for any
	TargetType  TargetType // ANY, CIDR, DOMAIN, DOMAIN_SUFFIX
	TargetValue string     // e.g. "10.20.0.0/16" or "example.com"
	PortStart   uint16
	PortEnd     uint16
	ipNet       *net.IPNet
}

type Policy struct {
	ID                  string
	Name                string
	AllowInternet       bool
	AllowPrivateNetwork bool
	AllowLoopback       bool // Allow localhost/loopback for testing/debugging
	Rules               []Rule

	// AccessMode, AccessHosts and AccessCIDRs form an explicit destination
	// allowlist ("allow") or denylist ("deny"). Empty mode = no gate.
	//
	// AccessHosts entries match hostnames before DNS resolution. Each entry is
	// one of:
	//   - a wildcard glob ("*.example.com", "api.??.com")  — contains * or ?
	//   - a domain suffix (".example.com")                 — leading dot
	//   - an exact domain ("example.com")                  — otherwise
	// AccessCIDRs entries match IP destinations and support a CIDR block
	// ("10.0.0.0/8"), a single IP ("1.2.3.4"), or an inclusive range
	// ("10.0.0.1-10.0.0.255").
	AccessMode  AccessMode
	AccessHosts []string
	AccessCIDRs []string
}

var (
	// Default dangerous subnets blocked unconditionally
	dangerousSubnets = []*net.IPNet{
		mustParseCIDR("127.0.0.0/8"),
		mustParseCIDR("169.254.0.0/16"),
		mustParseCIDR("224.0.0.0/4"),
		mustParseCIDR("::1/128"),
		mustParseCIDR("fe80::/10"),
	}

	// RFC 1918 Private subnets
	privateSubnets = []*net.IPNet{
		mustParseCIDR("10.0.0.0/8"),
		mustParseCIDR("172.16.0.0/12"),
		mustParseCIDR("192.168.0.0/16"),
		mustParseCIDR("fc00::/7"),
	}
)

func mustParseCIDR(s string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("invalid CIDR in code: %s: %v", s, err))
	}
	return ipnet
}

// Checker evaluates network access requests
type Checker struct {
	policy Policy

	// Compiled access list (see Policy.AccessMode). Nil/empty means no gate.
	accessMode  AccessMode
	accessHosts []func(string) bool
	accessCIDRs []*net.IPNet
	accessRngs  []ipRange
}

// Policy returns an independent, serializable snapshot for an authenticated
// relay to bind to an exit request. It never exposes mutable compiled state.
func (c *Checker) Policy() Policy {
	p := c.policy
	p.Rules = slices.Clone(p.Rules)
	p.AccessHosts = slices.Clone(p.AccessHosts)
	p.AccessCIDRs = slices.Clone(p.AccessCIDRs)
	for i := range p.Rules {
		p.Rules[i].ipNet = nil
	}
	return p
}

func NewChecker(p Policy) (*Checker, error) {
	p.Rules = slices.Clone(p.Rules)
	p.AccessHosts = slices.Clone(p.AccessHosts)
	p.AccessCIDRs = slices.Clone(p.AccessCIDRs)
	// Pre-parse CIDR rules
	for i := range p.Rules {
		r := &p.Rules[i]
		if r.Action != ActionAllow && r.Action != ActionDeny {
			return nil, fmt.Errorf("ACL rule %d: invalid action %q", i, r.Action)
		}
		r.Protocol = strings.ToLower(strings.TrimSpace(r.Protocol))
		if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" {
			return nil, fmt.Errorf("ACL rule %d: invalid protocol %q", i, r.Protocol)
		}
		if r.PortEnd != 0 && (r.PortStart == 0 || r.PortEnd < r.PortStart) {
			return nil, fmt.Errorf("ACL rule %d: invalid port range", i)
		}
		switch r.TargetType {
		case TargetAny, TargetCIDR:
		case TargetDomain, TargetDomainSuffix, TargetGlob:
			if strings.TrimSpace(r.TargetValue) == "" {
				return nil, fmt.Errorf("ACL rule %d: empty domain condition", i)
			}
		default:
			return nil, fmt.Errorf("ACL rule %d: invalid target type %q", i, r.TargetType)
		}
		if r.TargetType == TargetCIDR {
			ipNet, err := parseIPNet(r.TargetValue)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR or IP in rule %q: %w", r.TargetValue, err)
			}
			r.ipNet = ipNet
		}
	}

	// Sort rules by Priority descending (higher priority evaluates first)
	sort.SliceStable(p.Rules, func(i, j int) bool {
		return p.Rules[i].Priority > p.Rules[j].Priority
	})

	c := &Checker{policy: p}

	// Compile the access list. Accept "allow"/"deny" case-insensitively.
	switch AccessMode(strings.ToLower(strings.TrimSpace(string(p.AccessMode)))) {
	case AccessModeAllow, AccessModeDeny:
		c.accessMode = AccessMode(strings.ToLower(strings.TrimSpace(string(p.AccessMode))))
	case AccessModeNone:
	default:
		return nil, fmt.Errorf("invalid access mode %q", p.AccessMode)
	}

	for _, h := range p.AccessHosts {
		f, err := compileHostMatcher(h)
		if err != nil {
			return nil, fmt.Errorf("invalid access host %q: %w", h, err)
		}
		c.accessHosts = append(c.accessHosts, f)
	}
	for _, s := range p.AccessCIDRs {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("empty access CIDR/IP")
		}
		if strings.Contains(s, "-") {
			rng, err := parseIPRange(s)
			if err != nil {
				return nil, fmt.Errorf("invalid access IP range %q: %w", s, err)
			}
			c.accessRngs = append(c.accessRngs, rng)
			continue
		}
		ipNet, err := parseIPNet(s)
		if err != nil {
			return nil, fmt.Errorf("invalid access CIDR/IP %q: %w", s, err)
		}
		c.accessCIDRs = append(c.accessCIDRs, ipNet)
	}

	return c, nil
}

// parseIPNet parses a CIDR block or a single IP into a *net.IPNet.
func parseIPNet(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if ip, ipNet, err := net.ParseCIDR(s); err == nil {
		_ = ip
		return ipNet, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("not a CIDR or IP address")
	}
	if ip.To4() != nil {
		_, ipNet, _ := net.ParseCIDR(s + "/32")
		return ipNet, nil
	}
	_, ipNet, _ := net.ParseCIDR(s + "/128")
	return ipNet, nil
}

// CheckHost evaluates before DNS resolution (domain / ip string and port) for TCP.
func (c *Checker) CheckHost(ctx context.Context, host string, port uint16) error {
	return c.CheckHostProtocol(ctx, host, port, "tcp")
}

// CheckHostProtocol evaluates host/port with an explicit protocol ("tcp"/"udp").
func (c *Checker) CheckHostProtocol(ctx context.Context, host string, port uint16, proto string) error {
	// Destination access-list gate. This phase is type-aware: a hostname is
	// matched against AccessHosts (glob/suffix/exact) while a raw IP is matched
	// against AccessCIDRs.
	if err := c.accessCheckHost(host); err != nil {
		return err
	}

	ip := net.ParseIP(host)
	if ip != nil {
		return c.CheckIPProtocol(ctx, ip, port, proto)
	}

	// It's a domain name
	domainLower := strings.ToLower(host)
	for _, r := range c.policy.Rules {
		if !c.matchProtocol(r, proto) || !c.matchPort(r, port) {
			continue
		}
		if r.TargetType == TargetDomain && strings.ToLower(r.TargetValue) == domainLower {
			if r.Action == ActionDeny {
				return fmt.Errorf("domain %s denied by ACL policy", host)
			}
			return nil
		}
		if r.TargetType == TargetDomainSuffix {
			suffix := strings.ToLower(r.TargetValue)
			if !strings.HasPrefix(suffix, ".") {
				suffix = "." + suffix
			}
			if strings.HasSuffix(domainLower, suffix) || domainLower == strings.TrimPrefix(suffix, ".") {
				if r.Action == ActionDeny {
					return fmt.Errorf("domain suffix %s denied by ACL policy", host)
				}
				return nil
			}
		}
		if r.TargetType == TargetGlob {
			if matchGlob(strings.ToLower(r.TargetValue), domainLower) {
				if r.Action == ActionDeny {
					return fmt.Errorf("domain %s denied by ACL policy (glob %s)", host, r.TargetValue)
				}
				return nil
			}
		}
	}

	return nil
}

// CheckIP evaluates after DNS resolution (or direct IP destination) for TCP.
func (c *Checker) CheckIP(ctx context.Context, ip net.IP, port uint16) error {
	return c.CheckIPProtocol(ctx, ip, port, "tcp")
}

// CheckIPProtocol evaluates IP/port with an explicit protocol ("tcp"/"udp").
func (c *Checker) CheckIPProtocol(ctx context.Context, ip net.IP, port uint16, proto string) error {
	// 1. Unconditionally reject dangerous subnets unless AllowLoopback is true
	if !c.policy.AllowLoopback {
		for _, blocked := range dangerousSubnets {
			if blocked.Contains(ip) {
				return fmt.Errorf("access to dangerous subnet %s is blocked", ip.String())
			}
		}
	}

	// 1b. Denylist gate for resolved IPs. This only fires in "deny" mode and lets
	// a hostname request be blocked when it resolves into a denied range.
	if err := c.accessCheckIP(ip); err != nil {
		return err
	}

	// 2. Private network check
	isPrivate := false
	for _, priv := range privateSubnets {
		if priv.Contains(ip) {
			isPrivate = true
			break
		}
	}

	if isPrivate && !c.policy.AllowPrivateNetwork {
		return errors.New("access to private network is not allowed by policy")
	}

	if !isPrivate && !c.policy.AllowInternet {
		return errors.New("access to internet is not allowed by policy")
	}

	// 3. Evaluate specific CIDR rules
	for _, r := range c.policy.Rules {
		if !c.matchProtocol(r, proto) || !c.matchPort(r, port) {
			continue
		}
		if r.TargetType == TargetCIDR && r.ipNet != nil {
			if r.ipNet.Contains(ip) {
				if r.Action == ActionDeny {
					return fmt.Errorf("ip %s denied by ACL policy", ip.String())
				}
				return nil
			}
		}
		if r.TargetType == TargetAny {
			if r.Action == ActionDeny {
				return errors.New("request denied by catch-all rule")
			}
			return nil
		}
	}

	return nil
}

func (c *Checker) matchProtocol(r Rule, proto string) bool {
	if r.Protocol == "" {
		return true
	}
	return strings.EqualFold(r.Protocol, proto)
}

func (c *Checker) matchPort(r Rule, port uint16) bool {
	if r.PortStart == 0 && r.PortEnd == 0 {
		return true
	}
	if r.PortEnd == 0 {
		return port == r.PortStart
	}
	return port >= r.PortStart && port <= r.PortEnd
}

// ---------------------------------------------------------------------------
// Access-list (allowlist / denylist) matching
// ---------------------------------------------------------------------------

// accessCheckHost applies the access-list gate to a host string (domain or IP).
// It is type-aware: domains match AccessHosts, IPs match AccessCIDRs.
func (c *Checker) accessCheckHost(host string) error {
	if c.accessMode == AccessModeNone {
		return nil
	}
	matched := c.matchAccessHost(host)
	switch c.accessMode {
	case AccessModeAllow:
		if !matched {
			return fmt.Errorf("access to %s is not in the allow-list", host)
		}
	case AccessModeDeny:
		if matched {
			return fmt.Errorf("access to %s is denied by the deny-list", host)
		}
	}
	return nil
}

// accessCheckIP applies the access-list gate to a resolved IP. It only acts in
// "deny" mode: a whitelist is enforced at the host stage (type-aware) and must
// not re-reject a domain's resolved public IP here.
func (c *Checker) accessCheckIP(ip net.IP) error {
	if c.accessMode != AccessModeDeny {
		return nil
	}
	if c.matchAccessIP(ip) {
		return fmt.Errorf("ip %s is denied by the deny-list", ip.String())
	}
	return nil
}

// matchAccessHost reports whether a host string is covered by the access list.
func (c *Checker) matchAccessHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return c.matchAccessIP(ip)
	}
	lower := strings.ToLower(host)
	for _, m := range c.accessHosts {
		if m(lower) {
			return true
		}
	}
	return false
}

// matchAccessIP reports whether an IP is covered by the access CIDRs/ranges.
func (c *Checker) matchAccessIP(ip net.IP) bool {
	for _, ipNet := range c.accessCIDRs {
		if ipNet.Contains(ip) {
			return true
		}
	}
	for _, rng := range c.accessRngs {
		if rng.contains(ip) {
			return true
		}
	}
	return false
}

// compileHostMatcher builds a matcher for one AccessHosts entry. The entry is
// interpreted as a glob when it contains '*' or '?', as a suffix when it starts
// with '.', and as an exact domain otherwise.
func compileHostMatcher(pattern string) (func(string) bool, error) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, fmt.Errorf("empty host pattern")
	}
	switch {
	case strings.ContainsAny(pattern, "*?"):
		return func(host string) bool { return matchGlob(pattern, host) }, nil
	case strings.HasPrefix(pattern, "."):
		return func(host string) bool {
			return host == strings.TrimPrefix(pattern, ".") || strings.HasSuffix(host, pattern)
		}, nil
	default:
		return func(host string) bool { return host == pattern }, nil
	}
}

// matchGlob matches a glob pattern ('*' = any run, '?' = single char) against a
// string. Byte-oriented, which is correct for ASCII hostnames.
func matchGlob(pattern, s string) bool {
	p, ss := 0, 0
	star, match := -1, 0
	for ss < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[ss]):
			p++
			ss++
		case p < len(pattern) && pattern[p] == '*':
			star = p
			match = ss
			p++
		case star != -1:
			p = star + 1
			match++
			ss = match
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// ipRange is an inclusive IPv4 or IPv6 range.
type ipRange struct {
	from, to net.IP
}

func parseIPRange(s string) (ipRange, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return ipRange{}, fmt.Errorf("range must be start-end")
	}
	from := net.ParseIP(strings.TrimSpace(parts[0]))
	to := net.ParseIP(strings.TrimSpace(parts[1]))
	if from == nil || to == nil {
		return ipRange{}, fmt.Errorf("range endpoints must be IP addresses")
	}
	from = from.To16()
	to = to.To16()
	if from == nil || to == nil || bytes.Compare(from, to) > 0 {
		return ipRange{}, fmt.Errorf("range endpoints out of order or mismatched family")
	}
	return ipRange{from: from, to: to}, nil
}

func (r ipRange) contains(ip net.IP) bool {
	ip = ip.To16()
	if ip == nil {
		return false
	}
	return bytes.Compare(r.from, ip) <= 0 && bytes.Compare(ip, r.to) <= 0
}
