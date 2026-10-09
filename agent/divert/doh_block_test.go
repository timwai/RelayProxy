package divert

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestDoHBlocklistIsExactAndCaseInsensitive(t *testing.T) {
	for _, host := range []string{"DNS.GOOGLE.", "dns.quad9.net", "mozilla.cloudflare-dns.com", "DNS.NEXTDNS.IO"} {
		if !isKnownDoHEndpoint(host) {
			t.Fatalf("known resolver not identified: %s", host)
		}
	}
	for _, host := range []string{"www.google.com", "notdns.google", "dns.google.attacker.net", "", "cloudflare.com"} {
		if isKnownDoHEndpoint(host) {
			t.Fatalf("unrelated HTTPS host was blocked: %s", host)
		}
	}
}

func TestDoHBlockingHonorsOptInAndDoesNotBlockRegularWeb(t *testing.T) {
	enabled := true
	s := newTestServer(t, Options{
		Config:            Config{DefaultAction: ActionProxy},
		FakeIPEnabled:     func() bool { return true },
		BlockDoHEndpoints: func() bool { return enabled },
	})
	for _, test := range []struct {
		host   string
		port   uint16
		action Action
	}{
		{"dns.google", 443, ActionReject},
		{"dns.google", 8443, ActionProxy},
		{"www.google.com", 443, ActionProxy},
	} {
		ip, ok := s.fakeDNS.allocate(test.host, dnsmessage.TypeA)
		if !ok {
			t.Fatal("allocation failed")
		}
		flow := testFlow(ProtoTCP, nil)
		flow.IP = ip.String()
		flow.Port = test.port
		route, err := s.ClassifyFlow(flow)
		if err != nil || route.Decision().Action != test.action {
			t.Fatalf("%s:%d: %+v %v", test.host, test.port, route, err)
		}
	}
	enabled = false
	ip, _ := s.fakeDNS.allocate("dns.quad9.net", dnsmessage.TypeA)
	flow := testFlow(ProtoTCP, nil)
	flow.IP = ip.String()
	route, err := s.ClassifyFlow(flow)
	if err != nil || route.Decision().Action != ActionProxy {
		t.Fatalf("disabling DoH guard did not restore regular routing: %+v %v", route, err)
	}
	if !isFakeIP(netip.MustParseAddr(ip.String())) {
		t.Fatal("test did not exercise FakeIP")
	}
}
