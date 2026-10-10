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
	proxyDNSDoHPort          uint16 = 443
	proxyDNSOverallTimeout         = 10 * time.Second
	proxyDNSDoHAttemptTimeout      = 2 * time.Second
)

// Pinned IPs avoid recursive bootstrap DNS on the Agent. The TLS SNI and
// certificate must match each provider's published DoH hostname. All traffic
// is carried by the selected proxy exit; no local fallback is permitted.
type proxyDNSUpstream struct {
	name, address, host, url string
}

var proxyDNSDoHUpstreams = [...]proxyDNSUpstream{
	{"Cloudflare", "1.1.1.1", "cloudflare-dns.com", "https://cloudflare-dns.com/dns-query"},
	{"Google", "8.8.8.8", "dns.google", "https://dns.google/dns-query"},
	{"Quad9", "9.9.9.9", "dns.quad9.net", "https://dns.quad9.net/dns-query"},
}

// exchangeProxyDNS tries independent HTTPS resolvers through one selected
// exit; Quad9 is not a required bootstrap dependency. Keep negative replies
// (NXDOMAIN) authoritative, but retry transient upstream SERVFAIL. Every
// attempt is time-bounded; there is never a system DNS/plaintext fallback.
func (s *Server) exchangeProxyDNS(ctx context.Context, routeExitID string, question []byte) ([]byte, error) {
	var failures []string
	for _, upstream := range proxyDNSDoHUpstreams {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		reply, err := s.exchangeProxyDoHUpstream(ctx, routeExitID, question, upstream)
		if err == nil {
			var message dnsmessage.Message
			if unpackErr := message.Unpack(reply); unpackErr == nil && message.RCode == dnsmessage.RCodeServerFailure {
				err = errors.New("upstream returned SERVFAIL")
			} else {
				return reply, nil
			}
		}
		failures = append(failures, upstream.name+": "+err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply, err := s.exchangeProxyDoT(ctx, routeExitID, question)
	if err == nil {
		return reply, nil
	}
	failures = append(failures, "DoT/853: "+err.Error())
	return nil, fmt.Errorf("proxy encrypted DNS unavailable: %s", strings.Join(failures, "; "))
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
		log.Printf("[divert] proxy DNS resolution failed (exit=%q, encrypted upstreams exhausted): %v", s.dnsExitScope(""), err)
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

func (s *Server) exchangeProxyDoHUpstream(parent context.Context, routeExitID string, question []byte, upstream proxyDNSUpstream) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDNSDoHAttemptTimeout)
	defer cancel()
	exit := s.dnsExitScope(routeExitID)
	if s.opts.ProxyReady != nil && !s.opts.ProxyReady() &&
		(s.opts.LocalExitReady == nil || !s.opts.LocalExitReady(exit)) {
		return nil, errDNSForwardUnavailable
	}
	if s.dialer == nil {
		return nil, errDNSForwardUnavailable
	}
	transport := newProxyDoHTransport(s, exit, upstream)
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return proxyDoHRequest(ctx, client, upstream.url, question)
}

func newProxyDoHTransport(s *Server, exit string, upstream proxyDNSUpstream) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		// The dialer receives a literal resolver IP over the chosen exit.
		// Never resolve upstream.host locally or use environment proxy settings.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != net.JoinHostPort(upstream.host, "443") {
				return nil, fmt.Errorf("unexpected DoH destination %s/%s", network, address)
			}
			return s.dialer.DialTCP(ctx, exit, upstream.address, proxyDNSDoHPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: upstream.host,
		},
		TLSHandshakeTimeout:    proxyDNSDoHAttemptTimeout,
		ResponseHeaderTimeout:  proxyDNSDoHAttemptTimeout,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 16 << 10,
	}
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
