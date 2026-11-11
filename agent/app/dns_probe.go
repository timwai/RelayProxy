package app

import (
	"context"
	"errors"
	"strings"

	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
)

func (a *Agent) dnsPolicyForHost(host string) routing.DNSPolicy {
	p := a.routingEngine.DNSPolicy(host)
	// Explicit routing DNS follows the traffic default even when the user
	// keeps a separate encrypted DNS exit for legacy DNS-only rules.
	if p.Source == "routing" && p.ExitID == "" && (p.Resolver == "encrypted" || p.Resolver == "exit_system") {
		p.ExitID = a.rawDialer.GetDefaultExitID()
	}
	return p
}

// ProbeDNS uses the active transparent-proxy dialer and DNS policy. Even
// when DNS interception is disabled, diagnostics use the pinned DNS exit
// rather than performing unprotected system DNS lookups.
func (a *Agent) ProbeDNS(ctx context.Context) (divert.DNSProbeReport, error) {
	if a == nil {
		return divert.DNSProbeReport{}, errors.New("agent unavailable")
	}
	a.mu.RLock()
	srv := a.dnsSrv
	closed := a.closed.Load()
	a.mu.RUnlock()
	if closed || srv == nil {
		return divert.DNSProbeReport{}, errors.New("DNS Agent is not running")
	}
	return srv.ProbeDNS(ctx)
}

// ProbeDNSRule tests the effective saved DNS policy. Index -1 accepts any
// hostname; other indexes require a hostname matching that saved DNS rule.
func (a *Agent) ProbeDNSRule(ctx context.Context, index int, domain string) (divert.DNSRuleProbeReport, error) {
	if a == nil {
		return divert.DNSRuleProbeReport{}, errors.New("agent unavailable")
	}
	a.mu.RLock()
	srv := a.dnsSrv
	engine := a.routingEngine
	closed := a.closed.Load()
	a.mu.RUnlock()
	if closed || srv == nil || engine == nil {
		return divert.DNSRuleProbeReport{}, errors.New("DNS Agent is not running")
	}
	rules := engine.Config().DNSRules
	if index < -1 || index >= len(rules) {
		return divert.DNSRuleProbeReport{}, errors.New("DNS rule no longer exists; reload the saved configuration")
	}
	if index >= 0 {
		rule := rules[index]
		if !rule.Enabled {
			return divert.DNSRuleProbeReport{}, errors.New("disabled DNS rules cannot be tested as active rules")
		}
		domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
		matched := false
		for _, pattern := range rule.Domains {
			if routing.DNSRuleMatchesDomain(domain, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			return divert.DNSRuleProbeReport{}, errors.New("test domain must match this DNS rule")
		}
	}
	policy := a.dnsPolicyForHost(domain)
	report, err := srv.ProbeDNSPolicy(ctx, domain, policy.ExitID, policy.Resolver, policy.IP)
	report.Source, report.Rule = policy.Source, policy.Rule
	d := engine.DNSRoutingDecision(domain)
	report.RoutingRule, report.RoutingAction, report.TrafficExitID = d.Rule, string(d.Action), d.ExitID
	if d.Action == routing.ActionProxy && report.TrafficExitID == "" {
		report.TrafficExitID = a.rawDialer.GetDefaultExitID()
	}
	return report, err
}
