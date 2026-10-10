package divert

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestAutoDNSUsesProxyHTTPS443BeforeTLS853(t *testing.T) {
	var mu sync.Mutex
	var ports []uint16
	var exits []string
	s := newTestServer(t, Options{
		Config:          Config{DefaultAction: ActionProxy},
		ProxyDNSEnabled: func() bool { return true },
		ProxyReady:      func() bool { return true },
		DefaultExitID:   func() string { return "working-exit" },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			if host != "9.9.9.9" {
				t.Errorf("resolver was not pinned to an IP: %s", host)
			}
			mu.Lock()
			ports = append(ports, port)
			exits = append(exits, exit)
			mu.Unlock()
			return nil, errors.New("test tunnel unavailable")
		}},
	})
	query := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
	reply := fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), query, true))
	if reply.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("proxy failure must not leak local DNS: %v", reply.RCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ports) != 2 || ports[0] != 443 || ports[1] != 853 {
		t.Fatalf("wrong encrypted DNS fallback sequence: %v", ports)
	}
	if exits[0] != "working-exit" || exits[1] != "working-exit" {
		t.Fatalf("DNS did not stay on selected exit: %v", exits)
	}
	if len(s.fakeDNS.byIP) != 0 {
		t.Fatal("Auto DNS allocated FakeIP instead of genuine-IP DNS")
	}
}

func TestProxyDoHWirePostAndVerifiedResponse(t *testing.T) {
	query := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
	expected := authenticatedTestDNSResponse(t, query, netip.MustParseAddr("142.250.1.10"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/" {
			t.Errorf("unexpected DNS method or path: %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Content-Type") != "application/dns-message" ||
			req.Header.Get("Accept") != "application/dns-message" {
			t.Error("DoH headers are not RFC 8484 wire format")
		}
		body, err := io.ReadAll(req.Body)
		if err != nil || !bytes.Equal(body, query) {
			t.Errorf("DoH query altered: %v", err)
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(expected)
	}))
	defer server.Close()
	result, err := proxyDoHRequest(context.Background(), server.Client(), server.URL, query)
	if err != nil || !bytes.Equal(result, expected) {
		t.Fatalf("verified DoH response lost: %v", err)
	}
}

func TestProxyDoHRejectsUntrustedAndOversizedResponses(t *testing.T) {
	query := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
	another := fakeDNSQuestion(t, "untrusted.example", dnsmessage.TypeA)
	wrong := authenticatedTestDNSResponse(t, another, netip.MustParseAddr("203.0.113.45"))
	for _, tt := range []struct {
		name   string
		status int
		mime   string
		body   []byte
	}{
		{"wrong question", 200, "application/dns-message", wrong},
		{"wrong content type", 200, "text/plain", query},
		{"http error", 503, "application/dns-message", query},
		{"oversized", 200, "application/dns-message", bytes.Repeat([]byte{1}, proxyDNSMaximumReply+1)},
		{"redirect", 302, "application/dns-message", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.mime)
				w.WriteHeader(tt.status)
				_, _ = w.Write(tt.body)
			}))
			defer server.Close()
			client := server.Client()
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
			if _, err := proxyDoHRequest(context.Background(), client, server.URL, query); err == nil {
				t.Fatal("untrusted DoH result was accepted")
			}
		})
	}
}

func TestAutoDNSQueueWaitsInsteadOfImmediateSERVFAIL(t *testing.T) {
	var called bool
	var mu sync.Mutex
	s := newTestServer(t, Options{
		Config:     Config{DefaultAction: ActionProxy},
		ProxyReady: func() bool { return true },
		Dialer: &testDialer{tcp: func(_ context.Context, _, _ string, _ uint16) (net.Conn, error) {
			mu.Lock()
			called = true
			mu.Unlock()
			return nil, errors.New("proxy unavailable")
		}},
	})
	for n := 0; n < cap(s.dnsLimit); n++ {
		s.dnsLimit <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.exchangeLimitedProxyDNS(ctx, "", fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA))
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("DNS queue returned immediately instead of waiting for an available slot")
	case <-time.After(35 * time.Millisecond):
	}
	<-s.dnsLimit
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("DNS request did not resume when a slot became available")
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("released DNS slot never reached proxy resolver")
	}
}
