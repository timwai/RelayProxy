package divert

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// This is an explicit privacy tradeoff: TXT/SRV queries are sent to
	// Quad9 through the selected authenticated Relay/custom proxy exit.
	// The local machine never dials the DNS resolver or queries local DNS.
	proxyDNSResolverIP   = "9.9.9.9"
	proxyDNSResolverName = "dns.quad9.net"
	proxyDNSTimeout      = 4 * time.Second
	proxyDNSMaximumReply = 16384
)

var errDNSForwardUnavailable = errors.New("authenticated DNS forwarding unavailable")

func isForwardableDNSRecord(payload []byte) bool {
	var q dnsmessage.Message
	if err := q.Unpack(payload); err != nil || q.Response || q.OpCode != 0 || len(q.Questions) != 1 {
		return false
	}
	t := q.Questions[0].Type
	return q.Questions[0].Class == dnsmessage.ClassINET && (t == dnsmessage.TypeTXT || t == dnsmessage.TypeSRV)
}

func (s *Server) shouldForwardDNS(payload []byte) bool {
	return s != nil && s.fakeIPEnabled() && s.opts.ForwardOtherDNS != nil &&
		s.opts.ForwardOtherDNS() && isForwardableDNSRecord(payload)
}

// replyFakeDNS is a single cross-platform resolver entry point. TXT/SRV
// forwarding is opt-in and fails with SERVFAIL on any transport/auth error.
func (s *Server) replyFakeDNS(ctx context.Context, payload []byte) []byte {
	if !s.shouldForwardDNS(payload) {
		return s.fakeDNS.reply(payload, s.guard.RelayHost, s.guard.RelayIPs)
	}
	var query dnsmessage.Message
	if err := query.Unpack(payload); err != nil {
		return nil
	}
	var reply []byte
	var err error
	select {
	case s.dnsLimit <- struct{}{}:
		reply, err = s.exchangeProxyDoT(ctx, payload)
		<-s.dnsLimit
	default:
		err = errDNSForwardUnavailable
	}
	if err == nil {
		return reply
	}
	// Never switch to net.DefaultResolver or plaintext DNS if TLS fails.
	return packDNSResponse(dnsmessage.Message{
		Header: dnsmessage.Header{ID: query.ID, Response: true,
			RecursionDesired:   query.RecursionDesired,
			RecursionAvailable: true, RCode: dnsmessage.RCodeServerFailure},
		Questions: query.Questions,
	})
}

func (s *Server) exchangeProxyDoT(parent context.Context, question []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDNSTimeout)
	defer cancel()
	exit := ""
	if s.opts.DefaultExitID != nil {
		exit = s.opts.DefaultExitID()
	}
	if s.opts.ProxyReady != nil && !s.opts.ProxyReady() &&
		(s.opts.LocalExitReady == nil || !s.opts.LocalExitReady(exit)) {
		return nil, errDNSForwardUnavailable
	}
	raw, err := s.dialer.DialTCP(ctx, exit, proxyDNSResolverIP, 853)
	if err != nil {
		return nil, fmt.Errorf("proxy DNS TLS transport: %w", err)
	}
	if raw == nil {
		return nil, errDNSForwardUnavailable
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(proxyDNSTimeout))
	secure := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: proxyDNSResolverName})
	if err := secure.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("proxy DNS certificate/handshake verification: %w", err)
	}
	if len(question) > 65535 {
		return nil, fmt.Errorf("oversized DNS question")
	}
	wire := make([]byte, 2+len(question))
	binary.BigEndian.PutUint16(wire[:2], uint16(len(question)))
	copy(wire[2:], question)
	if _, err := secure.Write(wire); err != nil {
		return nil, err
	}
	var size [2]byte
	if _, err := io.ReadFull(secure, size[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(size[:]))
	if n < 12 || n > proxyDNSMaximumReply {
		return nil, fmt.Errorf("invalid DNS-over-TLS answer size: %d", n)
	}
	response := make([]byte, n)
	if _, err := io.ReadFull(secure, response); err != nil {
		return nil, err
	}
	var original, returned dnsmessage.Message
	if err := original.Unpack(question); err != nil {
		return nil, err
	}
	if err := returned.Unpack(response); err != nil || !returned.Response ||
		returned.ID != original.ID || len(returned.Questions) != 1 ||
		returned.Questions[0] != original.Questions[0] {
		return nil, fmt.Errorf("upstream DNS response does not match requested question")
	}
	return response, nil
}

// The dialer must be a selected Relay/custom-proxy dispatcher, never
// a direct network resolver.
