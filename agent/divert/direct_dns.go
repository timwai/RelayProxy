package divert

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// A private resolver address alone is never a DNS bypass: home routers are
// private too. Only a user-configured suffix permits plaintext local queries.
func (s *Server) directDNSQuestion(query []byte) bool {
	if s.opts.DNSDirectDomains == nil {
		return false
	}
	domains := s.opts.DNSDirectDomains()
	if len(domains) == 0 || isRelayDNSQuestion(query, s.loopGuard().RelayHost) {
		return false // Preserve the relay's verified bootstrap answers.
	}
	var question dnsmessage.Message
	if err := question.Unpack(query); err != nil || question.Response || question.Truncated ||
		question.OpCode != 0 || len(question.Questions) != 1 || question.Questions[0].Class != dnsmessage.ClassINET {
		return false
	}
	host := dnsName(question.Questions[0].Name)
	for _, domain := range domains {
		suffix := strings.ToLower(strings.TrimSuffix(domain, "."))
		if suffix != "" && (host == suffix || strings.HasSuffix(host, "."+suffix)) {
			return true
		}
	}
	return false
}

// TCP DNS is already terminated locally so each keep-alive query can follow
// its own DNS policy. Explicit exceptions use the client's original resolver;
// other queries continue using authenticated encrypted DNS/FakeIP.
func exchangeDirectDNSTCP(parent context.Context, resolver netip.AddrPort, query []byte) ([]byte, error) {
	if !resolver.IsValid() || resolver.Port() == 0 || isFakeIP(resolver.Addr()) {
		return nil, fmt.Errorf("original DNS resolver is unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, proxyDNSTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", resolver.String())
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(proxyDNSTimeout))
	wire := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(wire[:2], uint16(len(query)))
	copy(wire[2:], query)
	if err := writeNetworkBytes(conn, wire); err != nil {
		return nil, err
	}
	var prefix [2]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(prefix[:]))
	if size < 12 {
		return nil, fmt.Errorf("invalid direct DNS response size: %d", size)
	}
	answer := make([]byte, size)
	if _, err := io.ReadFull(conn, answer); err != nil {
		return nil, err
	}
	if err := validateProxyDNSResponse(query, answer); err != nil {
		return nil, err
	}
	return answer, nil
}
