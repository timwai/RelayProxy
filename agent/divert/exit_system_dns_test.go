package divert

import (
	"context"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestExitSystemDNSSelectsRuleExitAndReturnsRealAddress(t *testing.T) {
	callExit := ""
	calls := 0
	s := newTestServer(t, Options{
		ProxyDNSEnabled: func() bool { return true },
		ProxyReady:      func() bool { return false },
		DNSRoute: func(host string) (bool, string) {
			if host == "www.google.com" {
				return false, "us-exit"
			}
			return true, ""
		},
		DNSResolver: func(host string) string {
			if host == "www.google.com" {
				return "exit_system"
			}
			return "encrypted"
		},
		ExchangeExitSystemDNS: func(_ context.Context, exit string, query []byte) ([]byte, error) {
			calls++
			callExit = exit
			return authenticatedTestDNSResponse(t, query, netip.MustParseAddr("203.0.113.70")), nil
		},
	})
	query := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
	reply := fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), query, true))
	if callExit != "us-exit" || calls != 1 || len(reply.Answers) != 1 {
		t.Fatalf("system resolver not used: exit=%q calls=%d reply=%+v", callExit, calls, reply)
	}
	if got := netip.AddrFrom4(reply.Answers[0].Body.(*dnsmessage.AResource).A); got != netip.MustParseAddr("203.0.113.70") {
		t.Fatalf("system DNS reply changed: %s", got)
	}
}

func TestExitSystemDNSMissingTransportFailsClosed(t *testing.T) {
	s := newTestServer(t, Options{
		ProxyDNSEnabled: func() bool { return true },
		DNSRoute:        func(string) (bool, string) { return false, "bad-exit" },
		DNSResolver:     func(string) string { return "exit_system" },
	})
	q := fakeDNSQuestion(t, "private.example", dnsmessage.TypeAAAA)
	reply := fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), q, true))
	if reply.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("missing remote resolver did not fail closed: %+v", reply)
	}
}
