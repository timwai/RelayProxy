package divert

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestCustomDoHUpstreamsAreExclusiveAndKeepSelectedExit(t *testing.T) {
	var seenIPs []string
	var seenPorts []uint16
	var seenExits []string
	configured := []DNSUpstream{
		{URL: "https://dns.exit.example/dns-query", BootstrapIP: "10.0.0.53"},
		{URL: "https://backup.example/dns-query", BootstrapIP: "192.0.2.53"},
	}
	s := newTestServer(t, Options{
		Config: Config{DefaultAction: ActionProxy},
		ProxyReady: func() bool { return true },
		DNSExitID: func() string { return "exit-for-dns" },
		DNSUpstreams: func() []DNSUpstream { return configured },
		Dialer: &testDialer{tcp: func(_ context.Context, exit, host string, port uint16) (net.Conn, error) {
			seenExits = append(seenExits, exit)
			seenIPs = append(seenIPs, host)
			seenPorts = append(seenPorts, port)
			return nil, errors.New("test exit unreachable")
		}},
	})
	_, err := s.exchangeProxyDNS(context.Background(), "", fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA))
	if err == nil {
		t.Fatal("unreachable configured resolver should return error")
	}
	if !slices.Equal(seenIPs, []string{"10.0.0.53", "192.0.2.53"}) ||
		!slices.Equal(seenPorts, []uint16{443, 443}) {
		t.Fatalf("custom DNS fell back to a public resolver: ips=%v ports=%v", seenIPs, seenPorts)
	}
	for _, exit := range seenExits {
		if exit != "exit-for-dns" {
			t.Fatalf("custom DNS escaped pinned exit: %v", seenExits)
		}
	}
	configured = nil
	fallback, custom := s.activeProxyDNSUpstreams()
	if custom || len(fallback) != 3 || fallback[0].name != "Cloudflare" {
		t.Fatalf("empty custom config did not restore built-in defaults: %+v", fallback)
	}
}

func TestCustomDoHTransportVerifiesConfiguredTLSHostname(t *testing.T) {
	s := newTestServer(t, Options{Config: Config{DefaultAction: ActionProxy}})
	custom := proxyDNSUpstream{
		name: "dns.exit.example", host: "dns.exit.example",
		address: "10.0.0.53", url: "https://dns.exit.example/dns-query",
	}
	transport := newProxyDoHTransport(s, "exit-for-dns", custom)
	if transport.TLSClientConfig.InsecureSkipVerify ||
		transport.TLSClientConfig.ServerName != "dns.exit.example" ||
		transport.Proxy != nil || !transport.ForceAttemptHTTP2 {
		t.Fatal("custom DoH transport lacks certificate verification or permits direct proxy fallback")
	}
}
