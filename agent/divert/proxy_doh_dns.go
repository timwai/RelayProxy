package divert

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// RFC 8484 wire-format DNS over HTTPS. The HTTPS hostname is validated
	// against Quad9's certificate; the TCP destination never uses local DNS.
	proxyDNSDoHURL                = "https://dns.quad9.net/dns-query"
	proxyDNSDoHPort        uint16 = 443
	proxyDNSOverallTimeout        = 9 * time.Second
)

// exchangeProxyDNS prefers HTTPS/443. Many proxy exits cannot reach 853,
// and failing a system DNS/53 interception request would otherwise return
// SERVFAIL for every browser hostname. Both attempts use the selected exit.
// Neither attempt ever touches net.DefaultResolver or plaintext DNS.
func (s *Server) exchangeProxyDNS(ctx context.Context, routeExitID string, question []byte) ([]byte, error) {
	reply, dohErr := s.exchangeProxyDoH(ctx, routeExitID, question)
	if dohErr == nil {
		return reply, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply, dotErr := s.exchangeProxyDoT(ctx, routeExitID, question)
	if dotErr == nil {
		return reply, nil
	}
	return nil, fmt.Errorf("proxy encrypted DNS unavailable (DoH/443: %v; DoT/853: %w)", dohErr, dotErr)
}

// Log only one summary per interval: a resolver outage can otherwise emit
// thousands of SERVFAIL responses and overwhelm the service logs.
func (s *Server) reportProxyDNSError(err error) {
	if s == nil || err == nil {
		return
	}
	now := time.Now().Unix()
	previous := s.lastDNSFailureLog.Load()
	if now-previous >= 30 && s.lastDNSFailureLog.CompareAndSwap(previous, now) {
		log.Printf("[divert] proxy DNS resolution failed (exit=%q, DoH/443 then DoT/853): %v", s.dnsExitScope(""), err)
	}
}

// Wait for a bounded DNS slot rather than returning immediate SERVFAIL if
// a browser starts more than 16 DNS queries at once. Cancellation and a hard
// overall timeout still bound both the queue and the encrypted attempts.
func (s *Server) exchangeLimitedProxyDNS(parent context.Context, routeExitID string, question []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDNSOverallTimeout)
	defer cancel()
	select {
	case s.dnsLimit <- struct{}{}:
		defer func() { <-s.dnsLimit }()
		return s.exchangeProxyDNS(ctx, routeExitID, question)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) exchangeProxyDoH(parent context.Context, routeExitID string, question []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDNSTimeout)
	defer cancel()
	exit := s.dnsExitScope(routeExitID)
	if s.opts.ProxyReady != nil && !s.opts.ProxyReady() &&
		(s.opts.LocalExitReady == nil || !s.opts.LocalExitReady(exit)) {
		return nil, errDNSForwardUnavailable
	}
	if s.dialer == nil {
		return nil, errDNSForwardUnavailable
	}
	transport := &http.Transport{
		Proxy: nil,
		// Do NOT let the HTTP library resolve dns.quad9.net via the system
		// resolver or environment HTTP_PROXY variables.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != net.JoinHostPort(proxyDNSResolverName, "443") {
				return nil, fmt.Errorf("unexpected DoH destination %s/%s", network, address)
			}
			return s.dialer.DialTCP(ctx, exit, proxyDNSResolverIP, proxyDNSDoHPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: proxyDNSResolverName,
		},
		TLSHandshakeTimeout:    proxyDNSTimeout,
		ResponseHeaderTimeout:  proxyDNSTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxResponseHeaderBytes: 16 << 10,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return proxyDoHRequest(ctx, client, proxyDNSDoHURL, question)
}

// proxyDoHRequest is separate from transport creation to test content type,
// response validation and upper bounds without using a public DNS server.
func proxyDoHRequest(ctx context.Context, client *http.Client, url string, question []byte) ([]byte, error) {
	if len(question) < 12 || len(question) > 65535 {
		return nil, errors.New("invalid DNS question length")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(question))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/dns-message")
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("proxy DNS HTTPS request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("proxy DNS HTTPS status %d", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/dns-message") {
		return nil, fmt.Errorf("proxy DNS HTTPS returned invalid content type %q", response.Header.Get("Content-Type"))
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, proxyDNSMaximumReply+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > proxyDNSMaximumReply {
		return nil, errors.New("proxy DNS HTTPS answer too large")
	}
	if err := validateProxyDNSResponse(question, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// DoT and DoH share the same response validation; never accept a response
// for a different transaction, type or domain from an upstream endpoint.
func validateProxyDNSResponse(question, response []byte) error {
	var original, returned dnsmessage.Message
	if err := original.Unpack(question); err != nil || original.Response || len(original.Questions) != 1 {
		return errors.New("invalid upstream DNS question")
	}
	if err := returned.Unpack(response); err != nil || !returned.Response ||
		returned.ID != original.ID || returned.OpCode != original.OpCode ||
		len(returned.Questions) != 1 || returned.Questions[0] != original.Questions[0] {
		return errors.New("upstream DNS response does not match requested question")
	}
	return nil
}
