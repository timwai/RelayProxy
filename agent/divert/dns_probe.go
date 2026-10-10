package divert

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DNSProbeResult reports a real encrypted DNS request over the configured
// Relay/custom exit, not a direct TCP port check from the Agent machine.
type DNSProbeResult struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Address   string `json:"address"`
	Protocol  string `json:"protocol"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latencyMs"`
	RCode     string `json:"rcode,omitempty"`
	Error     string `json:"error,omitempty"`
}

type DNSProbeReport struct {
	ExitID  string           `json:"exitId"`
	Custom  bool             `json:"custom"`
	Results []DNSProbeResult `json:"results"`
}

// ProbeDNS performs real RFC8484 A-record queries through the currently
// configured DNS exit. It never uses the system resolver, plaintext DNS,
// application routing rules, or an unconfigured fallback resolver.
func (s *Server) ProbeDNS(ctx context.Context) (DNSProbeReport, error) {
	if s == nil || s.dialer == nil {
		return DNSProbeReport{}, fmt.Errorf("transparent DNS proxy is unavailable")
	}
	upstreams, custom := s.activeProxyDNSUpstreams()
	if len(upstreams) == 0 {
		return DNSProbeReport{}, fmt.Errorf("no valid encrypted DNS upstream")
	}
	name, err := dnsmessage.NewName("example.com.")
	if err != nil {
		return DNSProbeReport{}, err
	}
	query := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: 12457, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	wire, err := query.Pack()
	if err != nil {
		return DNSProbeReport{}, err
	}
	report := DNSProbeReport{
		ExitID: s.dnsExitScope(""), Custom: custom,
		Results: make([]DNSProbeResult, 0, len(upstreams)+1),
	}
	for _, upstream := range upstreams {
		if ctx.Err() != nil {
			break
		}
		result := DNSProbeResult{
			Name: upstream.name, URL: upstream.url,
			Address: upstream.address, Protocol: "DoH/443",
		}
		start := time.Now()
		answer, queryErr := s.exchangeProxyDoHUpstream(ctx, "", wire, upstream)
		result.LatencyMS = time.Since(start).Milliseconds()
		if queryErr == nil {
			var parsed dnsmessage.Message
			queryErr = parsed.Unpack(answer)
			if queryErr == nil {
				result.RCode = parsed.RCode.String()
				if parsed.RCode != dnsmessage.RCodeSuccess {
					queryErr = fmt.Errorf("DNS response: %s", parsed.RCode)
				}
			}
		}
		result.OK = queryErr == nil
		if queryErr != nil {
			result.Error = queryErr.Error()
		}
		report.Results = append(report.Results, result)
		if ctx.Err() != nil {
			break
		}
	}
	if !custom && ctx.Err() == nil {
		start := time.Now()
		answer, queryErr := s.exchangeProxyDoT(ctx, "", wire)
		result := DNSProbeResult{
			Name: "Quad9", URL: "tls://dns.quad9.net:853",
			Address: proxyDNSResolverIP, Protocol: "DoT/853",
			LatencyMS: time.Since(start).Milliseconds(),
		}
		if queryErr == nil {
			var parsed dnsmessage.Message
			queryErr = parsed.Unpack(answer)
			if queryErr == nil {
				result.RCode = parsed.RCode.String()
				if parsed.RCode != dnsmessage.RCodeSuccess {
					queryErr = fmt.Errorf("DNS response: %s", parsed.RCode)
				}
			}
		}
		result.OK = queryErr == nil
		if queryErr != nil {
			result.Error = queryErr.Error()
		}
		report.Results = append(report.Results, result)
	}
	return report, nil
}
