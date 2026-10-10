package divert

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// A Chrome DNS question sent to a private resolver must be captured without
// FakeIP. With no authenticated exit, return SERVFAIL instead of leaking the
// query to the configured local resolver or allocating a synthetic address.
func TestRealProxyDNSCapturesSystemUDPWithoutFakeIP(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		Config:          Config{DefaultAction: ActionProxy},
		FakeIPEnabled:   func() bool { return false },
		ProxyDNSEnabled: func() bool { return true },
		ProxyReady:      func() bool { return false },
	})
	syn := interceptedSYN(false)
	original, err := parseIPPacket(syn)
	if err != nil {
		t.Fatal(err)
	}
	packet, _ := packetTestFixture(false, ProtoUDP, fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA), false)
	resolver := netip.MustParseAddrPort("192.168.1.1:53")
	if err := rewriteIPPacket(packet, original.Source, resolver); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(packet, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	out := expectInterceptedPacket(t, device)
	dns, err := parseIPPacket(out.data)
	if err != nil {
		t.Fatal(err)
	}
	if out.meta.outbound || dns.Source != resolver || dns.Destination.Addr() != original.Source.Addr() {
		t.Fatalf("DNS escaped private resolver or lost tuple: %+v %+v", out.meta, dns)
	}
	answer := fakeDNSAnswer(t, dns.Payload)
	if answer.RCode != dnsmessage.RCodeServerFailure || len(answer.Answers) != 0 {
		t.Fatalf("unavailable proxy should fail closed: %+v", answer)
	}
	if len(i.server.fakeDNS.byIP) != 0 {
		t.Fatal("real proxy DNS allocated FakeIP")
	}
}

// Association is an independent switch. The same verified UDP response only
// attributes an IP when the user left DNS association enabled.
func TestAssociationToggleDoesNotDependOnFakeIP(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		i, _ := newTestInterceptor(t, Options{
			Config:                Config{DefaultAction: ActionProxy},
			DNSAssociationEnabled: func() bool { return enabled },
		})
		client := netip.MustParseAddrPort("192.0.2.10:51000")
		server := netip.MustParseAddrPort("192.0.2.53:53")
		query := fakeDNSQuestion(t, "www.google.com", dnsmessage.TypeA)
		reply := authenticatedTestDNSResponse(t, query, netip.MustParseAddr("142.250.1.10"))
		if i.server.dnsAssociationEnabled() {
			i.dns.query(client, server, query)
			i.dns.response(server, client, reply)
		}
		flow := i.flowMetadata(ipPacket{Destination: netip.MustParseAddrPort("142.250.1.10:443")}, packetProcess{})
		if enabled && (flow.Host != "www.google.com" || flow.DomainSource != "dns") {
			t.Fatalf("DNS association lost real IP hostname: %+v", flow)
		}
		if !enabled && (flow.Host != "" || flow.DomainSource != "") {
			t.Fatalf("disabled DNS association attributed hostname: %+v", flow)
		}
	}
}
