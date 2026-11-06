package divert

import (
	"context"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDomainDNSRoutingSelectsLocalAndRemoteExit(t *testing.T) {
	calls := 0
	s := newTestServer(t, Options{
		ProxyDNSEnabled: func() bool { return true },
		DNSRoute: func(host string) (bool, string) {
			calls++
			if host == "google.com" || host == "www.google.com" {
				return false, "us-exit"
			}
			return true, ""
		},
		Guard:      LoopGuard{RelayHost: "relay.example", RelayIPs: []string{"198.51.100.4"}},
		ProxyReady: func() bool { return false },
	})
	remote := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
	local := fakeDNSQuestion(t, "www.baidu.com", dnsmessage.TypeAAAA)
	if s.directDNSQuestion(remote) || !s.directDNSQuestion(local) {
		t.Fatal("local/remote DNS routing did not select correct resolution side")
	}
	if got := s.remoteDNSExit(remote); got != "us-exit" {
		t.Fatalf("DNS rule exit not honored: %q", got)
	}
	if s.directDNSQuestion(fakeDNSQuestion(t, "relay.example", dnsmessage.TypeA)) {
		t.Fatal("relay bootstrap hostname was forwarded to system DNS")
	}
	if s.directDNSQuestion([]byte("malformed")) {
		t.Fatal("malformed DNS question escaped to the local resolver")
	}
	if calls == 0 {
		t.Fatal("DNS routing callback not consulted")
	}
	// If a platform accidentally sends a LOCAL question to the remote path,
	// refuse it with SERVFAIL instead of leaking it to the wrong resolver.
	answer := fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), local, true))
	if answer.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("expected SERVFAIL, got %v", answer.RCode)
	}
	// Remote names with an unavailable exit also fail closed, never use local DNS.
	answer = fakeDNSAnswer(t, s.interceptedDNSReply(context.Background(), remote, true))
	if answer.RCode != dnsmessage.RCodeServerFailure {
		t.Fatalf("remote DNS failed open: %v", answer.RCode)
	}
}

func TestDomainDNSRuleUDPUsesLocalOriginalResolver(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		ProxyDNSEnabled: func() bool { return true },
		DNSRoute: func(host string) (bool, string) {
			if host == "api.google.com" {
				return false, "us-exit"
			}
			return true, ""
		},
		ProxyReady: func() bool { return false },
	})
	query := fakeDNSQuestion(t, "www.baidu.com", dnsmessage.TypeA)
	packet, _ := packetTestFixture(false, ProtoUDP, query, false)
	parsed, _ := parseIPPacket(packet)
	resolver := netip.MustParseAddrPort("192.168.1.1:53")
	if err := rewriteIPPacket(packet, parsed.Source, resolver); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(packet, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	forwarded := expectInterceptedPacket(t, device)
	got, err := parseIPPacket(forwarded.data)
	if err != nil {
		t.Fatal(err)
	}
	if !forwarded.meta.outbound || got.Destination != resolver {
		t.Fatalf("unmatched domain did not keep original system DNS destination: %+v", got)
	}
}
