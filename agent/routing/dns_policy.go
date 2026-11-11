package routing

import (
	"strconv"
	"strings"
)

// DNSPolicy describes resolution before the application has connected. System
// DNS supplies a hostname, but no reliable application process or service port.
type DNSPolicy struct {
	Action   DNSAction
	Resolver string
	ExitID   string
	IP       string
	Rule     string
	Source   string
}

func (e *Engine) DNSRoutingDecision(host string) Decision {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dnsDecisionLocked(strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), ".")))
}

func (e *Engine) dnsDecisionLocked(host string) Decision {
	if e.config.Mode != ModeRule {
		return e.decideFlowLocked(Flow{Host: host})
	}
	flow := Flow{Host: host}
	if ip, ok := e.dnsHosts[host]; ok {
		flow.IP = ip.String()
	}
	for i, rule := range e.config.Rules {
		if !rule.Enabled || len(rule.Processes) > 0 || len(rule.Entries) > 0 || len(rule.SourceCIDRs) > 0 || len(rule.Ports) > 0 {
			continue
		}
		// The editor's TCP + UDP setting covers both transports and does not
		// constrain a hostname-only DNS decision. A single transport does.
		if len(rule.Protocols) > 0 {
			tcp, udp, all := false, false, false
			for _, p := range rule.Protocols {
				tcp = tcp || strings.EqualFold(p, "tcp")
				udp = udp || strings.EqualFold(p, "udp")
				all = all || p == "*"
			}
			if !all && !(tcp && udp) {
				continue
			}
			rule.Protocols = nil
		}
		if e.compound[i].matches(rule, flow) {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = "rule #" + strconv.Itoa(i+1)
			}
			return Decision{Action: rule.Action, ExitID: rule.ExitID, Rule: name, Matched: true, DNSResolver: rule.DNSResolver, DNSIP: rule.DNSIP}
		}
	}
	for _, item := range e.subscriptions {
		if item.config.Enabled && item.status.Rules > 0 && !item.excluded.matches(flow) && item.included.matches(flow) {
			action := item.config.Action
			if action == "" {
				action = ActionProxy
			}
			return Decision{Action: action, ExitID: item.config.ExitID, Rule: "订阅：" + item.config.Name, Matched: true}
		}
	}
	return Decision{Action: e.config.DefaultAction, Rule: "default"}
}

func (e *Engine) DNSPolicy(host string) DNSPolicy {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	e.mu.RLock()
	defer e.mu.RUnlock()
	d := e.dnsDecisionLocked(host)
	return e.dnsPolicyLocked(host, d)
}

func (e *Engine) dnsPolicyLocked(host string, d Decision) DNSPolicy {
	if e.config.DNSFollowRouting && d.Action == ActionReject {
		return dnsPolicyForDecision(d)
	}
	if d.DNSResolver != "" {
		return dnsPolicyForDecision(d)
	}
	if ip, ok := e.dnsHosts[host]; ok {
		return DNSPolicy{Action: DNSActionLocal, Resolver: "static", IP: ip.String(), Source: "host_override"}
	}
	for _, suffix := range e.config.DNSDirectDomains {
		if matchDomainSuffix(host, strings.TrimSuffix(suffix, ".")) {
			return DNSPolicy{Action: DNSActionLocal, Resolver: "local", Source: "local_exception"}
		}
	}
	for _, rule := range e.config.DNSRules {
		if !rule.Enabled {
			continue
		}
		for _, pattern := range rule.Domains {
			if matchDNSRuleDomain(host, pattern) {
				exitID := rule.ExitID
				if exitID == "" && e.config.DNSFollowRouting && d.Action == ActionProxy {
					exitID = d.ExitID
				}
				resolver := rule.Resolver
				if rule.Action == DNSActionLocal {
					return DNSPolicy{Action: DNSActionLocal, Resolver: "local", Rule: rule.Name, Source: "dns_rule"}
				}
				if resolver == "" {
					resolver = "encrypted"
				}
				return DNSPolicy{Action: rule.Action, Resolver: resolver, ExitID: exitID, Rule: rule.Name, Source: "dns_rule"}
			}
		}
	}
	if e.config.DNSFollowRouting {
		return dnsPolicyForDecision(d)
	}
	if len(e.config.DNSRules) > 0 {
		return DNSPolicy{Action: DNSActionLocal, Resolver: "local", Source: "default"}
	}
	return DNSPolicy{Action: DNSActionRemote, Resolver: "encrypted", Source: "default"}
}

func dnsPolicyForDecision(d Decision) DNSPolicy {
	p := DNSPolicy{Action: DNSActionRemote, Resolver: d.DNSResolver, ExitID: d.ExitID, IP: d.DNSIP, Rule: d.Rule, Source: "routing"}
	if d.Action == ActionReject {
		p.Action = DNSActionReject
		p.Resolver = "reject"
		return p
	}
	switch p.Resolver {
	case "static":
		p.Action = DNSActionLocal
		p.ExitID = ""
	case "local":
		p.Action = DNSActionLocal
		p.ExitID = ""
	case "encrypted", "exit_system":
	default:
		if d.Action == ActionDirect {
			p.Action = DNSActionLocal
			p.Resolver = "local"
			p.ExitID = ""
		} else {
			p.Resolver = "encrypted"
		}
	}
	return p
}
