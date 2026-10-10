package divert

import (
	"context"

	"golang.org/x/net/dns/dnsmessage"
)

// interceptedDNSReply keeps the FakeIP and genuine-IP resolver paths separate.
// DNS/53 response injection always happens on the original destination tuple.
func (s *Server) interceptedDNSReply(ctx context.Context, query []byte, udp bool) []byte {
	if s.fakeIPEnabled() {
		return s.replyFakeDNS(ctx, query)
	}
	if !s.proxyDNSEnabled() {
		return nil
	}
	// Relay authentication must bootstrap without relying on its own proxy.
	// For its configured hostname, use the already verified relay addresses.
	if isRelayDNSQuestion(query, s.guard.RelayHost) {
		return s.fakeDNS.replyScoped(query, s.guard.RelayHost, s.guard.RelayIPs, "", false)
	}
	return realProxyDNSReply(ctx, query, udp, func(ctx context.Context, query []byte) ([]byte, error) {
		select {
		case s.dnsLimit <- struct{}{}:
			defer func() { <-s.dnsLimit }()
			return s.exchangeProxyDoT(ctx, "", query)
		default:
			return nil, errDNSForwardUnavailable
		}
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

// realProxyDNSReply validates a complete response from authenticated DoT.
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
