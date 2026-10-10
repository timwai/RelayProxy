package divert

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestAuthenticatedDNSExitPrecedenceAndFailClosed(t *testing.T) {
	calls := []string{}
	dnsExit := "dedicated-dns"
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionProxy},
		DefaultExitID: func() string { return "ordinary-default" },
		DNSExitID:     func() string { return dnsExit },
		ProxyReady:    func() bool { return true },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			calls = append(calls, exit)
			if host != proxyDNSResolverIP || port != 853 {
				t.Errorf("DoT never allowed via non-designated endpoint: %s:%d", host, port)
			}
			return nil, errors.New("expected offline test")
		}},
	})
	q := fakeDNSQuestion(t, "example.org", dnsmessage.TypeA)
	if _, err := s.exchangeProxyDoT(context.Background(), "rule-selected-exit", q); err == nil {
		t.Fatal("DoT transport unexpectedly succeeded")
	}
	if _, err := s.exchangeProxyDoT(context.Background(), "", q); err == nil {
		t.Fatal("DoT transport unexpectedly succeeded")
	}
	dnsExit = ""
	if _, err := s.exchangeProxyDoT(context.Background(), "", q); err == nil {
		t.Fatal("DoT transport unexpectedly succeeded")
	}
	for i, expected := range []string{"rule-selected-exit", "dedicated-dns", "ordinary-default"} {
		if calls[i] != expected {
			t.Fatalf("query %d used %q instead of %q", i, calls[i], expected)
		}
	}
}

func TestDNSExitChosenForUnattributedTXTQueries(t *testing.T) {
	dialed := ""
	s := newTestServer(t, Options{
		Config:          Config{DefaultAction: ActionProxy},
		FakeIPEnabled:   func() bool { return true },
		ForwardOtherDNS: func() bool { return true },
		DNSExitID:       func() string { return "isolated-dns-exit" },
		ProxyReady:      func() bool { return true },
		DefaultExitID:   func() string { return "app-default-exit" },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			dialed = exit
			return nil, errors.New("offline")
		}},
	})
	reply := fakeDNSAnswer(t, s.replyFakeDNS(context.Background(), fakeDNSQuestion(t, "example.org", dnsmessage.TypeTXT)))
	if reply.RCode != dnsmessage.RCodeServerFailure || dialed != "isolated-dns-exit" {
		t.Fatalf("unattributed TXT query used wrong exit or leaked: %q %+v", dialed, reply.Header)
	}
}
