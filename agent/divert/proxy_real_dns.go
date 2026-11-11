package divert

import (
	"context"
	"fmt"
	"net"

	"golang.org/x/net/dns/dnsmessage"
)

// interceptedDNSReply keeps the FakeIP and genuine-IP resolver paths separate.
// DNS/53 response injection always happens on the original destination tuple.
func (s *Server) interceptedDNSReply(ctx context.Context, query []byte, udp bool) []byte {
	if !isRelayDNSQuestion(query, s.loopGuard().RelayHost) {
		if reply := s.dnsHostOverrideReply(query); reply != nil {
			return reply
		}
	}
	if s.fakeIPEnabled() {
		return s.replyFakeDNS(ctx, query)
	}
	if !s.proxyDNSEnabled() {
		// Preserve the legacy isolated FakeIP responder test contract.
		if s.opts.FakeIPEnabled == nil && s.opts.ProxyDNSEnabled == nil {
			return s.replyFakeDNS(ctx, query)
		}
		return nil
	}
	if isRelayDNSQuestion(query, s.loopGuard().RelayHost) {
		// Relay authentication bootstraps from verified transport addresses.
		return s.fakeDNS.replyScoped(query, s.loopGuard().RelayHost, s.loopGuard().RelayIPs, "", false)
	}
	if s.opts.DNSBlocked != nil {
		var q dnsmessage.Message
		if q.Unpack(query) == nil && len(q.Questions) == 1 && s.opts.DNSBlocked(dnsName(q.Questions[0].Name)) {
			return packDNSResponse(dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RecursionDesired: q.RecursionDesired, RecursionAvailable: true, RCode: dnsmessage.RCodeRefused}, Questions: q.Questions})
		}
	}
	if reply := s.dnsHostOverrideReply(query); reply != nil {
		return reply
	}
	// Packet interception and the DNS/TCP resolver normally bypass LOCAL
	// questions first. Fail closed if a platform cannot reinject local DNS.
	if s.directDNSQuestion(query) {
		return realProxyDNSReply(ctx, query, udp, nil)
	}
	exitID := s.remoteDNSExit(query)
	useExitSystem := false
	if s.opts.DNSResolver != nil {
		var question dnsmessage.Message
		if err := question.Unpack(query); err == nil && len(question.Questions) == 1 &&
			(question.Questions[0].Type == dnsmessage.TypeA || question.Questions[0].Type == dnsmessage.TypeAAAA) {
			useExitSystem = s.opts.DNSResolver(dnsName(question.Questions[0].Name)) == "exit_system"
		}
	}
	return realProxyDNSReply(ctx, query, udp, func(ctx context.Context, query []byte) ([]byte, error) {
		var response []byte
		var err error
		if useExitSystem {
			response, err = s.exchangeLimitedSystemDNS(ctx, exitID, query)
		} else {
			response, err = s.exchangeLimitedProxyDNS(ctx, exitID, query)
		}
		if err != nil {
			s.reportProxyDNSError(err)
		}
		return response, err
	})
}

func isRelayDNSQuestion(payload []byte, relayHost string) bool {
	if relayHost == "" {
		return false
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(payload); err != nil || msg.Response || len(msg.Questions) != 1 {
		return false
	}
	q := msg.Questions[0]
	return q.Class == dnsmessage.ClassINET && (q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeAAAA) &&
		dnsName(q.Name) == dnsNameFromString(relayHost)
}

func dnsNameFromString(host string) string {
	name, err := dnsmessage.NewName(host)
	if err != nil {
		name, err = dnsmessage.NewName(host + ".")
	}
	if err != nil {
		return ""
	}
	return dnsName(name)
}

// realProxyDNSReply validates a complete response from authenticated DoH/DoT.
// Failure never triggers plaintext/system DNS; it returns a DNS SERVFAIL.
// For large UDP answers it sets TC for retry over intercepted TCP/53.
func realProxyDNSReply(ctx context.Context, raw []byte, udp bool, exchange func(context.Context, []byte) ([]byte, error)) []byte {
	var question dnsmessage.Message
	if err := question.Unpack(raw); err != nil || question.Response || question.OpCode != 0 ||
		question.Truncated || len(question.Questions) != 1 || question.Questions[0].Class != dnsmessage.ClassINET {
		return nil
	}
	servfail := func() []byte {
		return packDNSResponse(dnsmessage.Message{
			Header: dnsmessage.Header{
				ID: question.ID, Response: true, RecursionDesired: question.RecursionDesired,
				RecursionAvailable: true, RCode: dnsmessage.RCodeServerFailure,
			},
			Questions: question.Questions,
		})
	}
	if ctx.Err() != nil || exchange == nil {
		return servfail()
	}
	reply, err := exchange(ctx, raw)
	if err != nil || ctx.Err() != nil {
		return servfail()
	}
	var response dnsmessage.Message
	if err := response.Unpack(reply); err != nil || !response.Response || response.ID != question.ID ||
		response.OpCode != 0 || len(response.Questions) != 1 ||
		response.Questions[0] != question.Questions[0] {
		return servfail()
	}
	if udp && len(reply) > 1232 {
		return packDNSResponse(dnsmessage.Message{
			Header: dnsmessage.Header{
				ID: question.ID, Response: true, RecursionDesired: question.RecursionDesired,
				RecursionAvailable: response.RecursionAvailable, Truncated: true, RCode: response.RCode,
			},
			Questions: question.Questions,
		})
	}
	return reply
}

// ExchangeGatewayDNS reuses authenticated encrypted DNS without enabling local capture.
func (s *Server) ExchangeGatewayDNS(ctx context.Context, exitID string, query []byte, udp bool) []byte {
	if reply := s.dnsHostOverrideReply(query); reply != nil {
		return reply
	}
	var msg dnsmessage.Message
	if msg.Unpack(query) != nil || len(msg.Questions) != 1 {
		return nil
	}
	question := msg.Questions[0]
	host := dnsName(question.Name)
	if s.opts.DNSBlocked != nil && s.opts.DNSBlocked(host) {
		return packDNSResponse(dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true, RCode: dnsmessage.RCodeRefused}, Questions: msg.Questions})
	}
	local := false
	if s.opts.DNSRoute != nil {
		var selected string
		local, selected = s.opts.DNSRoute(host)
		if selected != "" {
			exitID = selected
		}
	}
	return realProxyDNSReply(ctx, query, udp, func(ctx context.Context, q []byte) ([]byte, error) {
		if local {
			network := "ip4"
			if question.Type == dnsmessage.TypeAAAA {
				network = "ip6"
			} else if question.Type != dnsmessage.TypeA {
				return nil, fmt.Errorf("gateway local DNS supports A/AAAA queries")
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, network, host)
			if err != nil {
				return nil, err
			}
			reply := dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true, RecursionDesired: msg.RecursionDesired, RecursionAvailable: true}, Questions: msg.Questions}
			for _, ip := range ips {
				header := dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, Type: question.Type, TTL: dnsHostOverrideTTL}
				if question.Type == dnsmessage.TypeA && ip.Is4() {
					reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: header, Body: &dnsmessage.AResource{A: ip.As4()}})
				}
				if question.Type == dnsmessage.TypeAAAA && ip.Is6() {
					reply.Answers = append(reply.Answers, dnsmessage.Resource{Header: header, Body: &dnsmessage.AAAAResource{AAAA: ip.As16()}})
				}
			}
			return reply.Pack()
		}
		if s.opts.DNSResolver != nil && s.opts.DNSResolver(host) == "exit_system" && (question.Type == dnsmessage.TypeA || question.Type == dnsmessage.TypeAAAA) {
			return s.exchangeLimitedSystemDNS(ctx, exitID, q)
		}
		return s.exchangeLimitedProxyDNS(ctx, exitID, q)
	})
}

// exchangeLimitedSystemDNS shares the encrypted DNS concurrency budget while
// selecting the remote OS resolver. It never falls back to local DNS or DoH.
func (s *Server) exchangeLimitedSystemDNS(parent context.Context, routeExitID string, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDNSOverallTimeout)
	defer cancel()
	select {
	case s.dnsLimit <- struct{}{}:
		defer func() { <-s.dnsLimit }()
		if s.opts.ExchangeExitSystemDNS == nil {
			return nil, fmt.Errorf("remote exit system DNS is unavailable")
		}
		return s.opts.ExchangeExitSystemDNS(ctx, s.systemDNSExitScope(routeExitID), query)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
