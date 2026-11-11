package divert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type DNSRuleProbeResult struct {
	Type      string   `json:"type"`
	OK        bool     `json:"ok"`
	RCode     string   `json:"rcode,omitempty"`
	Answers   []string `json:"answers"`
	LatencyMS int64    `json:"latencyMs"`
	Error     string   `json:"error,omitempty"`
}

type DNSRuleProbeReport struct {
	Rule          string               `json:"rule,omitempty"`
	RoutingRule   string               `json:"routingRule,omitempty"`
	RoutingAction string               `json:"routingAction,omitempty"`
	TrafficExitID string               `json:"trafficExitId,omitempty"`
	Domain        string               `json:"domain"`
	ExitID        string               `json:"exitId,omitempty"`
	Resolver      string               `json:"resolver"`
	Source        string               `json:"source"`
	Results       []DNSRuleProbeResult `json:"results"`
}

// ProbeDNSRule preserves the rule diagnostic API with static overrides.
func (s *Server) ProbeDNSRule(ctx context.Context, domain, routeExitID, resolver string) (DNSRuleProbeReport, error) {
	staticIP := ""
	if ip, ok := s.dnsHostOverride(domain); ok {
		staticIP = ip.String()
		resolver = "static"
	}
	return s.ProbeDNSPolicy(ctx, domain, routeExitID, resolver, staticIP)
}

// ResolveDNS resolves a connection using its already matched rule and DNS exit.
// No hostname-only policy is reevaluated here.
func (s *Server) ResolveDNS(ctx context.Context, exitID, domain, resolver string) (string, error) {
	report, err := s.probeDNSPolicy(ctx, domain, exitID, resolver, "", true)
	if err != nil {
		return "", err
	}
	var failures []error
	for _, r := range report.Results {
		if r.OK && len(r.Answers) > 0 {
			return r.Answers[0], nil
		}
		if r.Error != "" {
			failures = append(failures, errors.New(r.Error))
		}
	}
	return "", fmt.Errorf("DNS for %s returned no addresses: %w", domain, errors.Join(append(failures, errors.New("no A/AAAA answer"))...))
}

// ProbeDNSPolicy uses the same exchange functions as captured DNS and returns
// each family's addresses, response code and latency, including empty answers.
func (s *Server) ProbeDNSPolicy(ctx context.Context, domain, routeExitID, resolver, staticIP string) (DNSRuleProbeReport, error) {
	return s.probeDNSPolicy(ctx, domain, routeExitID, resolver, staticIP, false)
}

func (s *Server) probeDNSPolicy(ctx context.Context, domain, routeExitID, resolver, staticIP string, firstAddressOnly bool) (DNSRuleProbeReport, error) {
	if s == nil || s.dialer == nil {
		return DNSRuleProbeReport{}, fmt.Errorf("DNS proxy is unavailable")
	}
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	name, err := dnsmessage.NewName(domain + ".")
	if err != nil || domain == "" || strings.ContainsAny(domain, "* /:\\") {
		return DNSRuleProbeReport{}, fmt.Errorf("invalid DNS test domain")
	}
	if resolver == "" {
		resolver = "encrypted"
	}
	switch resolver {
	case "encrypted", "exit_system", "static", "local", "reject":
	default:
		return DNSRuleProbeReport{}, fmt.Errorf("invalid DNS rule resolver")
	}
	report := DNSRuleProbeReport{Domain: domain, Resolver: resolver, Source: "remote", Results: make([]DNSRuleProbeResult, 0, 2)}
	if resolver == "static" {
		ip, err := netip.ParseAddr(staticIP)
		if err != nil || ip.Zone() != "" {
			return report, fmt.Errorf("invalid static DNS address")
		}
		report.Source = "host_override"
		ip = ip.Unmap()
		report.Results = []DNSRuleProbeResult{
			{Type: "A", OK: true, RCode: dnsmessage.RCodeSuccess.String(), Answers: matchingOverrideAnswers(ip, true)},
			{Type: "AAAA", OK: true, RCode: dnsmessage.RCodeSuccess.String(), Answers: matchingOverrideAnswers(ip, false)},
		}
		return report, nil
	}
	if resolver == "exit_system" {
		report.ExitID = s.systemDNSExitScope(routeExitID)
	} else if resolver == "encrypted" {
		report.ExitID = s.dnsExitScope(routeExitID)
	} else {
		report.Source = resolver
	}
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		typeLabel, network := "A", "ip4"
		if kind == dnsmessage.TypeAAAA {
			typeLabel, network = "AAAA", "ip6"
		}
		result := DNSRuleProbeResult{Type: typeLabel, Answers: []string{}}
		start := time.Now()
		var queryErr error
		switch resolver {
		case "reject":
			result.RCode = dnsmessage.RCodeRefused.String()
			queryErr = fmt.Errorf("DNS rejected by routing policy")
		case "local":
			var ips []netip.Addr
			ips, queryErr = net.DefaultResolver.LookupNetIP(ctx, network, domain)
			if queryErr == nil {
				result.OK = true
				result.RCode = dnsmessage.RCodeSuccess.String()
				for _, ip := range ips {
					result.Answers = append(result.Answers, ip.String())
				}
			}
		default:
			query := dnsmessage.Message{Header: dnsmessage.Header{ID: uint16(s.dnsQueryID.Add(1)), RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: kind, Class: dnsmessage.ClassINET}}}
			var wire, answer []byte
			wire, queryErr = query.Pack()
			if queryErr == nil {
				if resolver == "exit_system" {
					answer, queryErr = s.exchangeLimitedSystemDNS(ctx, routeExitID, wire)
				} else {
					answer, queryErr = s.exchangeLimitedProxyDNS(ctx, routeExitID, wire)
				}
			}
			if queryErr == nil {
				queryErr = validateProxyDNSResponse(wire, answer)
			}
			if queryErr == nil {
				var response dnsmessage.Message
				queryErr = response.Unpack(answer)
				if queryErr == nil {
					result.RCode = response.RCode.String()
					result.OK = response.RCode == dnsmessage.RCodeSuccess || response.RCode == dnsmessage.RCodeNameError
					result.Answers = dnsAnswerValues(name, kind, response.Answers)
					if !result.OK {
						queryErr = fmt.Errorf("DNS response: %s", result.RCode)
					}
				}
			}
		}
		result.LatencyMS = time.Since(start).Milliseconds()
		if queryErr != nil {
			result.Error = queryErr.Error()
		}
		report.Results = append(report.Results, result)
		if firstAddressOnly && result.OK && len(result.Answers) > 0 {
			break
		}
	}
	return report, nil
}

func matchingOverrideAnswers(ip netip.Addr, ipv4 bool) []string {
	if ipv4 == ip.Is4() {
		return []string{ip.String()}
	}
	return []string{}
}

func dnsAnswerValues(name dnsmessage.Name, kind dnsmessage.Type, records []dnsmessage.Resource) []string {
	owner := dnsName(name)
	seen := make(map[string]struct{})
	for depth := 0; depth < 16; depth++ {
		next := ""
		for _, record := range records {
			if record.Header.Class != dnsmessage.ClassINET || dnsName(record.Header.Name) != owner {
				continue
			}
			if cname, ok := record.Body.(*dnsmessage.CNAMEResource); ok {
				next = dnsName(cname.CNAME)
				break
			}
		}
		if next == "" {
			break
		}
		if _, cycle := seen[next]; cycle {
			return []string{}
		}
		seen[owner] = struct{}{}
		owner = next
	}
	answers := make([]string, 0, len(records))
	seenIPs := make(map[string]struct{})
	for _, record := range records {
		if record.Header.Class != dnsmessage.ClassINET || record.Header.Type != kind || dnsName(record.Header.Name) != owner {
			continue
		}
		var address netip.Addr
		switch value := record.Body.(type) {
		case *dnsmessage.AResource:
			address = netip.AddrFrom4(value.A)
		case *dnsmessage.AAAAResource:
			address = netip.AddrFrom16(value.AAAA)
		}
		if !address.IsValid() {
			continue
		}
		value := address.String()
		if _, duplicate := seenIPs[value]; duplicate {
			continue
		}
		seenIPs[value] = struct{}{}
		answers = append(answers, value)
	}
	return answers
}
