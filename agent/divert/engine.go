package divert

import (
	"net/netip"
	"path"
	"strconv"
	"strings"
	"sync"
)

// Engine matches flows against process-first rules.
type Engine struct {
	mu    sync.RWMutex
	cfg   Config
	cidrs [][]netip.Prefix
	ports [][]portRange
}

type portRange struct {
	lo, hi uint16
}

// NewEngine builds an engine from config.
func NewEngine(cfg Config) (*Engine, error) {
	e := &Engine{}
	if err := e.Reload(cfg); err != nil {
		return nil, err
	}
	return e, nil
}

// Reload replaces rules atomically.
func (e *Engine) Reload(cfg Config) error {
	var err error
	cfg, err = validatedConfig(cfg)
	if err != nil {
		return err
	}
	cidrs := make([][]netip.Prefix, len(cfg.Rules))
	ports := make([][]portRange, len(cfg.Rules))
	for i, r := range cfg.Rules {
		cidrs[i] = parseCIDRs(r.CIDRs)
		ports[i] = parsePorts(r.Ports)
	}
	e.mu.Lock()
	e.cfg = cfg
	e.cidrs = cidrs
	e.ports = ports
	e.mu.Unlock()
	return nil
}

// Config returns a snapshot of the current config.
func (e *Engine) Config() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return cloneConfig(e.cfg)
}

// Match returns the divert decision for a flow.
func (e *Engine) Match(f Flow) Decision {
	return e.MatchWith(f, nil)
}

// MatchWith retains loop exclusions ahead of the agent's shared routing policy.
func (e *Engine) MatchWith(f Flow, shared func(Flow) Decision) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()

	proc := strings.TrimSpace(f.Process)
	for _, ex := range e.cfg.ExcludeProcesses {
		if matchProcess(ex, proc) {
			return Decision{Action: ActionDirect, Rule: "exclude"}
		}
	}
	if shared != nil {
		return shared(f)
	}

	proto := f.Protocol
	if proto == "" {
		proto = ProtoTCP
	}

	for i, r := range e.cfg.Rules {
		if !r.Enabled {
			continue
		}
		if !matchProcess(r.Process, proc) {
			continue
		}
		if !matchProtocols(r.Protocols, proto) {
			continue
		}
		if len(r.Hosts) > 0 && !matchHosts(r.Hosts, f.Host, f.IP) {
			continue
		}
		if len(e.cidrs[i]) > 0 && !matchCIDRs(e.cidrs[i], f.IP) {
			continue
		}
		if len(e.ports[i]) > 0 && !matchPortRanges(e.ports[i], f.Port) {
			continue
		}
		action := r.Action
		if action == "" {
			action = e.cfg.DefaultAction
		}
		return Decision{Action: action, ExitID: r.ExitID, Rule: r.Name, DatagramRequired: r.DatagramRequired}
	}

	return Decision{Action: e.cfg.DefaultAction, Rule: "default"}
}

func matchProtocols(list []string, proto Protocol) bool {
	if len(list) == 0 {
		return true
	}
	for _, p := range list {
		if strings.EqualFold(strings.TrimSpace(p), string(proto)) {
			return true
		}
	}
	return false
}

func matchHosts(patterns []string, host, ip string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		h = strings.ToLower(strings.TrimSpace(ip))
	}
	for _, pat := range patterns {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if pat == "" {
			continue
		}
		if pat == "*" || pat == h {
			return true
		}
		if strings.HasPrefix(pat, "*.") && (h == pat[2:] || strings.HasSuffix(h, pat[1:])) {
			return true
		}
		if strings.HasPrefix(pat, ".") && strings.HasSuffix(h, pat) {
			return true
		}
		if ok, _ := path.Match(pat, h); ok {
			return true
		}
	}
	return false
}

func parseCIDRs(in []string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			if p.Addr().Is4In6() && p.Bits() >= 96 {
				p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
			}
			out = append(out, p.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(s); err == nil {
			addr = addr.Unmap()
			bits := 32
			if addr.Is6() {
				bits = 128
			}
			out = append(out, netip.PrefixFrom(addr, bits))
		}
	}
	return out
}

func matchCIDRs(cidrs []netip.Prefix, ipStr string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(ipStr))
	if err != nil {
		return false
	}
	for _, c := range cidrs {
		if c.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}

func parsePorts(in []string) []portRange {
	var out []portRange
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(s, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 == nil && err2 == nil && a >= 0 && b <= 65535 && a <= b {
				out = append(out, portRange{lo: uint16(a), hi: uint16(b)})
			}
			continue
		}
		n, err := strconv.Atoi(s)
		if err == nil && n >= 0 && n <= 65535 {
			out = append(out, portRange{lo: uint16(n), hi: uint16(n)})
		}
	}
	return out
}

func matchPortRanges(ranges []portRange, port uint16) bool {
	for _, r := range ranges {
		if port >= r.lo && port <= r.hi {
			return true
		}
	}
	return false
}
