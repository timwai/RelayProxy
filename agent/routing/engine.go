package routing

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

// Engine evaluates the same ordered compound rules for every client entry.
type Engine struct {
	mu                      sync.RWMutex
	config                  Config
	compound                []compoundRule
	dnsHosts                map[string]netip.Addr
	subscriptions           []compiledSubscription
	subscriptionCancel      context.CancelFunc
	subscriptionProxyDialer subscriptionProxyDial
	subscriptionClosed      bool
}
type portRange struct{ start, end uint16 }

func NewEngine(cfg Config) (*Engine, error) {
	e, err := newEngineSnapshot(cfg)
	if err == nil {
		e.startSubscriptionUpdates()
	}
	return e, err
}

// newEngineSnapshot prepares a policy without starting background workers.
func newEngineSnapshot(cfg Config) (*Engine, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeGlobalProxy
	}
	if cfg.DNSMode == "" {
		cfg.DNSMode = DNSModeProxy
	}
	if cfg.DefaultAction == "" {
		cfg.DefaultAction = ActionProxy
	}
	cfg = CloneConfig(cfg)
	dnsHosts := make(map[string]netip.Addr, len(cfg.DNSHosts))
	for _, mapping := range cfg.DNSHosts {
		ip, _ := netip.ParseAddr(mapping.IP)
		domain := strings.ToLower(strings.TrimSuffix(mapping.Domain, "."))
		dnsHosts[domain] = ip.Unmap()
	}
	e := &Engine{config: cfg, compound: make([]compoundRule, len(cfg.Rules)), dnsHosts: dnsHosts}
	for i, rule := range cfg.Rules {
		e.compound[i] = compileCompound(rule)
	}
	for _, sub := range cfg.Subscriptions {
		e.subscriptions = append(e.subscriptions, cachedSubscription(sub))
	}
	return e, nil
}

func (e *Engine) Reload(cfg Config) error {
	compiled, err := newEngineSnapshot(cfg)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if e.subscriptionCancel != nil {
		e.subscriptionCancel()
		e.subscriptionCancel = nil
	}
	e.config, e.compound, e.dnsHosts, e.subscriptions = compiled.config, compiled.compound, compiled.dnsHosts, compiled.subscriptions
	e.mu.Unlock()
	e.startSubscriptionUpdates()
	return nil
}

// FakeIPEnabled is a hot-path, allocation-free read of the active DNS policy.
func (e *Engine) FakeIPEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.FakeIPEnabled && (e.config.DNSMode == "" || e.config.DNSMode == DNSModeProxy)
}

// DNSAssociationEnabled controls conservative hostname attribution from observed
// DNS responses. Absence in older configs preserves legacy behavior (enabled).
func (e *Engine) DNSAssociationEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.DNSAssociationEnabled == nil || *e.config.DNSAssociationEnabled
}

// ProxyDNSEnabled is independent of proxy hostname forwarding and FakeIP.
// Only captured system DNS/53 is resolved over authenticated proxy-side DoT.
func (e *Engine) ProxyDNSEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.ProxyDNSEnabled && !e.config.FakeIPEnabled
}

// DoHBlockedIPs returns a defensive copy of the configured explicit
// HTTPS/443 endpoint blocklist. The matcher only runs for port 443.
func (e *Engine) DoHBlockedIPs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.config.FakeIPEnabled || !e.config.BlockDoHEndpoints {
		return nil
	}
	return append([]string(nil), e.config.DoHBlockedIPs...)
}

// BlockDoHEndpoints is an opt-in best-effort policy; hostnames unavailable
// before connection cannot be blocked by this selector.
// ForwardOtherDNS never falls back to plaintext/local DNS. Queries travel
// through the selected authenticated relay/upstream to a TLS resolver.
// DNSUpstreams returns a defensive copy so policy updates cannot race with DNS queries.
func (e *Engine) DNSUpstreams() []DNSUpstream {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]DNSUpstream(nil), e.config.DNSUpstreams...)
}

func (e *Engine) DNSDirectDomains() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.config.DNSDirectDomains...)
}

// DNSHostIP returns an exact-domain static DNS override, if configured.
func (e *Engine) DNSHostIP(host string) (string, bool) {
	if e == nil {
		return "", false
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	e.mu.RLock()
	defer e.mu.RUnlock()
	d := e.dnsDecisionLocked(host)
	if d.DNSIP != "" {
		return d.DNSIP, true
	}
	if d.DNSResolver != "" || (e.config.DNSFollowRouting && d.Action == ActionReject) {
		return "", false
	}
	if ip, ok := e.dnsHosts[host]; ok {
		return ip.String(), true
	}
	return "", false
}

// DNSHostMappingsEnabled reports whether exact-domain DNS overrides are active.
func (e *Engine) DNSHostMappingsEnabled() bool {
	if e == nil {
		return false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.dnsHosts) > 0 {
		return true
	}
	if e.config.Mode == ModeRule {
		for _, r := range e.config.Rules {
			if r.Enabled && r.DNSIP != "" {
				return true
			}
		}
	}
	return false
}

// DNSRoute selects a DNS resolver before the client performs any lookup.
// Without DNS rules, legacy global proxy-DNS behavior is unchanged.
// With rules present, unmatched names intentionally use LOCAL resolution.
func (e *Engine) DNSRoute(host string) (local bool, exitID string) {
	if !e.ProxyDNSEnabled() {
		return false, ""
	}
	p := e.DNSPolicy(host)
	return p.Action == DNSActionLocal, p.ExitID
}

// DNSResolver returns the transport for a matched REMOTE DNS rule. Legacy
// policies and rules without a resolver select the established encrypted path.
func (e *Engine) DNSResolver(host string) string {
	if !e.ProxyDNSEnabled() {
		return "encrypted"
	}
	if e.DNSPolicy(host).Resolver == "exit_system" {
		return "exit_system"
	}
	return "encrypted"
}

func (e *Engine) DNSBlocked(host string) bool {
	return e.ProxyDNSEnabled() && e.DNSPolicy(host).Action == DNSActionReject
}

// DNSExitID supplies the legacy dedicated encrypted DNS exit. Routing-follow
// mode uses the matched traffic exit instead.
func (e *Engine) DNSExitID() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.config.DNSFollowRouting {
		return ""
	}
	return e.config.DNSExitID
}

func (e *Engine) ForwardOtherDNS() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.FakeIPEnabled && e.config.ForwardOtherDNS && e.config.DNSMode == DNSModeProxy
}

func (e *Engine) BlockDoHEndpoints() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.FakeIPEnabled && e.config.BlockDoHEndpoints && e.config.DNSMode == DNSModeProxy
}

func (e *Engine) Config() Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return CloneConfig(e.config)
}

func (e *Engine) Match(host string, port uint16) (Action, string) {
	d := e.Decide(host, port)
	return d.Action, d.ExitID
}
func (e *Engine) Decide(host string, port uint16) Decision {
	return e.DecideFlow(Flow{Host: host, Port: port, Protocol: "tcp"})
}
func (e *Engine) DecideFlow(flow Flow) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	host := strings.ToLower(strings.TrimSuffix(flow.Host, "."))
	ip, mapped := e.dnsHosts[host]
	if mapped && flow.IP == "" {
		flow.IP = ip.String()
	}
	d := e.decideFlowLocked(flow)
	if mapped && d.DNSResolver == "" {
		d.DNSResolver = "static"
		d.DNSIP = ip.String()
	}
	if e.config.DNSFollowRouting && d.DNSResolver == "" && host != "" && d.Action == ActionProxy {
		p := e.dnsPolicyLocked(host, d)
		d.DNSResolver, d.DNSIP, d.DNSExitID = p.Resolver, p.IP, p.ExitID
	}
	return d
}

func (e *Engine) decideFlowLocked(flow Flow) Decision {
	switch e.config.Mode {
	case ModeGlobalProxy:
		return Decision{Action: ActionProxy, Rule: "global_proxy"}
	case ModeDirect:
		return Decision{Action: ActionDirect, Rule: "direct"}
	}
	for i, rule := range e.config.Rules {
		if rule.Enabled && e.compound[i].matches(rule, flow) {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = "rule #" + strconv.Itoa(i+1)
			}
			return Decision{Action: rule.Action, ExitID: rule.ExitID, DatagramRequired: rule.DatagramRequired, HandleDirect: rule.HandleDirect, Rule: name, Matched: true, DNSResolver: rule.DNSResolver, DNSIP: rule.DNSIP}
		}
	}
	for _, item := range e.subscriptions {
		if !item.config.Enabled || item.status.Rules == 0 || item.excluded.matches(flow) || !item.included.matches(flow) {
			continue
		}
		action := item.config.Action
		if action == "" {
			action = ActionProxy
		}
		return Decision{Action: action, ExitID: item.config.ExitID, Rule: "订阅：" + item.config.Name, Matched: true}
	}
	return Decision{Action: e.config.DefaultAction, Rule: "default"}
}

func matchDomainSuffix(host, suffix string) bool {
	host, suffix = strings.ToLower(host), strings.ToLower(suffix)
	suffix = strings.TrimPrefix(suffix, ".")
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func parsePortRange(s string) portRange {
	if parts := strings.SplitN(s, "-", 2); len(parts) == 2 {
		start, _ := strconv.ParseUint(parts[0], 10, 16)
		end, _ := strconv.ParseUint(parts[1], 10, 16)
		return portRange{uint16(start), uint16(end)}
	}
	p, _ := strconv.ParseUint(s, 10, 16)
	return portRange{start: uint16(p)}
}
