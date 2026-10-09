package divert

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// Exercise packet injection and the same policy decision used by WinDivert and
// Linux NFQUEUE; no real DNS resolver or external socket is contacted.
func TestFakeIPDNSPacketInterceptionAndDomainRouting(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		Config: Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return true },
	})
	syn := interceptedSYN(false)
	original, err := parseIPPacket(syn)
	if err != nil {
		t.Fatal(err)
	}
	query, _ := packetTestFixture(false, ProtoUDP, fakeDNSQuestion(t, "Play.Google.Com", dnsmessage.TypeA), false)
	resolver := netip.MustParseAddrPort("192.168.1.1:53")
	if err := rewriteIPPacket(query, original.Source, resolver); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(query, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	answerPacket := expectInterceptedPacket(t, device)
	reply, err := parseIPPacket(answerPacket.data)
	if err != nil {
		t.Fatal(err)
	}
	if answerPacket.meta.outbound || reply.Source != resolver || reply.Destination.Addr() != original.Source.Addr() {
		t.Fatalf("fake DNS reply was not injected locally: %+v %+v", reply, answerPacket.meta)
	}
	dnsResponse := fakeDNSAnswer(t, reply.Payload)
	fake := netip.AddrFrom4(dnsResponse.Answers[0].Body.(*dnsmessage.AResource).A)
	if !isFakeIP(fake) {
		t.Fatalf("expected FakeIP, got %s", fake)
	}
	proxySYN := interceptedSYN(false)
	if err := rewriteIPPacket(proxySYN, original.Source, netip.AddrPortFrom(fake, 443)); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(proxySYN, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	proxyPacket := expectInterceptedPacket(t, device)
	if proxyPacket.meta.outbound {
		t.Fatal("FakeIP SYN bypassed transparent proxy")
	}
	key := FlowKey{Protocol: ProtoTCP, Source: original.Source, Destination: netip.AddrPortFrom(fake, 443)}
	i.mu.Lock()
	flow := i.tcp[key]
	i.mu.Unlock()
	if flow == nil || flow.route.flow.Host != "play.google.com" || flow.route.flow.DomainSource != "fakeip" {
		t.Fatalf("FakeIP was not restored to domain before classification: %+v", flow)
	}
	if got := proxyDialTarget(flow.route.flow); got != "play.google.com" {
		t.Fatalf("upstream received FakeIP instead of hostname: %s", got)
	}
}

func TestFakeIPUnknownAddressNeverReinjectedToNetwork(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		Config: Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return true },
	})
	syn := interceptedSYN(false)
	src, err := parseIPPacket(syn)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteIPPacket(syn, src.Source, netip.MustParseAddrPort("198.19.44.55:443")); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	p := expectInterceptedPacket(t, device)
	if p.meta.outbound {
		t.Fatal("unknown FakeIP escaped")
	}
	response, err := parseIPPacket(p.data)
	if err != nil || response.Protocol != ProtoTCP || response.TCPFlags&0x04 == 0 {
		t.Fatalf("unknown FakeIP was not reset: %+v %v", response, err)
	}
	if len(i.tcp) != 0 {
		t.Fatal("unrecognized FakeIP generated a proxied TCP flow")
	}
}

func TestFakeIPRejectsSystemDNSLeakWithoutLocalForwarding(t *testing.T) {
	i, device := newTestInterceptor(t, Options{
		Config: Config{DefaultAction: ActionDirect},
		FakeIPEnabled: func() bool { return true },
	})
	for _, port := range []uint16{853, 784, 8853} {
		udp, _ := packetTestFixture(false, ProtoUDP, []byte{1, 2}, false)
		p, _ := parseIPPacket(udp)
		target := netip.AddrPortFrom(p.Destination.Addr(), port)
		if err := rewriteIPPacket(udp, p.Source, target); err != nil {
			t.Fatal(err)
		}
		if err := i.handlePacket(udp, packetMetadata{outbound: true}); err != nil {
			t.Fatal(err)
		}
		select {
		case escaped := <-device.sent:
			t.Fatalf("DNS egress UDP/%d escaped: %+v", port, escaped)
		default:
		}
	}
}
