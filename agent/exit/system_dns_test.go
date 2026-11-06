package exit

import (
	"context"
	"errors"
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
)

func systemDNSQuestion(t *testing.T, kind dnsmessage.Type) []byte {
	t.Helper()
	name, err := dnsmessage.NewName("example.com.")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := (dnsmessage.Message{Header: dnsmessage.Header{ID: 144, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: name, Type: kind, Class: dnsmessage.ClassINET}}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestExitSystemDNSResolvesAAndAAAAWithoutOpenProxyPort(t *testing.T) {
	for _, tc := range []struct {
		kind    dnsmessage.Type
		network string
		ip      net.IP
	}{
		{dnsmessage.TypeA, "ip4", net.ParseIP("203.0.113.23")},
		{dnsmessage.TypeAAAA, "ip6", net.ParseIP("2001:db8::23")},
	} {
		calls := 0
		h := NewHandler(HandlerConfig{SystemDNSLookup: func(_ context.Context, network, host string) ([]net.IP, error) {
			calls++
			if network != tc.network || host != "example.com" {
				t.Errorf("unexpected lookup %s %s", network, host)
			}
			return []net.IP{tc.ip}, nil
		}})
		q := systemDNSQuestion(t, tc.kind)
		resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
		if err != nil || !resp.Success || calls != 1 {
			t.Fatalf("exit DNS failed: %+v err=%v calls=%d", resp, err, calls)
		}
		ips, err := protocol.ValidateExitDNSAnswer(q, resp.Answer)
		if err != nil || len(ips) != 1 || !ips[0].Equal(tc.ip) {
			t.Fatalf("unexpected DNS answer: ips=%v err=%v", ips, err)
		}
	}
}

func TestExitSystemDNSDoesNotResolveUnsupportedOrOversizedQueries(t *testing.T) {
	used := false
	h := NewHandler(HandlerConfig{SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		used = true
		return nil, errors.New("must not run")
	}})
	for _, q := range [][]byte{systemDNSQuestion(t, dnsmessage.TypeTXT), make([]byte, protocol.MaxExitDNSQuestionBytes+1)} {
		resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
		if err == nil || resp.Success || used {
			t.Fatalf("invalid query contacted resolver: err=%v resp=%+v", err, resp)
		}
	}
}

func TestExitSystemDNSRespectsExitACLBeforeAndAfterLookup(t *testing.T) {
	policy := acl.Policy{ID: "exit-dns-policy", AllowInternet: true, AllowPrivateNetwork: false, AllowLoopback: false}
	checker, err := acl.NewChecker(policy)
	if err != nil {
		t.Fatal(err)
	}
	lookups := 0
	h := NewHandler(HandlerConfig{ACLChecker: checker, SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		lookups++
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}})
	q := systemDNSQuestion(t, dnsmessage.TypeA)
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: q})
	if err == nil || resp.Success || lookups != 1 {
		t.Fatalf("denied address disclosed or lookup not called: resp=%+v err=%v lookups=%d", resp, err, lookups)
	}
}

func TestExitSystemDNSResolverFailuresDoNotFallback(t *testing.T) {
	calls := 0
	h := NewHandler(HandlerConfig{SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
		calls++
		return nil, errors.New("offline")
	}})
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{RequestID: "dns-1", Question: systemDNSQuestion(t, dnsmessage.TypeA)})
	if err == nil || resp.Success || calls != 1 {
		t.Fatalf("failure not closed: resp=%+v err=%v calls=%d", resp, err, calls)
	}
}

func TestExitSystemDNSBlocksRecursiveDNSCapture(t *testing.T) {
	called := false
	h := NewHandler(HandlerConfig{
		SystemDNSAllowed: func() bool { return false },
		SystemDNSLookup: func(context.Context, string, string) ([]net.IP, error) {
			called = true
			return []net.IP{net.ParseIP("203.0.113.23")}, nil
		},
	})
	resp, err := h.ResolveSystemDNS(context.Background(), protocol.ExitDNSRequest{
		RequestID: "dns-guard", Question: systemDNSQuestion(t, dnsmessage.TypeA),
	})
	if err == nil || resp.Success || called {
		t.Fatalf("recursive DNS capture guard failed: resp=%+v err=%v called=%t", resp, err, called)
	}
}
