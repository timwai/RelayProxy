package divert

import (
	"bytes"
	"net/netip"
	"testing"
)

// A complete DIRECT DNS/TCP response must be attributed before its packet
// returns to the Windows stack; the application's first SYN may follow it.
func TestPassiveDNSTCPAttributionPrecedesFirstSYN(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}})
	syn, err := parseIPPacket(interceptedSYN(false))
	if err != nil {
		t.Fatal(err)
	}
	resolver := netip.MustParseAddrPort("192.0.2.53:53")
	query, answer := dnsExchange(t, "first.example", syn.Destination.Addr(), 90)

	queryPacket, _ := packetTestFixture(false, ProtoTCP, dnsTCPWire(query), false)
	if err := rewriteIPPacket(queryPacket, syn.Source, resolver); err != nil {
		t.Fatal(err)
	}
	answerPacket, _ := packetTestFixture(false, ProtoTCP, dnsTCPWire(answer), false)
	if err := rewriteIPPacket(answerPacket, resolver, syn.Source); err != nil {
		t.Fatal(err)
	}
	seenQuery, seenAnswer := false, false
	i.device = &dnsOrderDevice{testPacketDevice: device, beforeSend: func(data []byte, meta packetMetadata) {
		if meta.outbound {
			i.dns.mu.Lock()
			pending := len(i.dns.pending)
			i.dns.mu.Unlock()
			if pending != 1 {
				t.Errorf("TCP DNS query was not cached before sending: %d", pending)
			}
			if !bytes.Equal(data, queryPacket) {
				t.Error("outbound DNS/TCP payload was changed")
			}
			seenQuery = true
			return
		}
		if host := i.dns.lookup(syn.Destination.Addr()); host != "first.example" {
			t.Errorf("TCP DNS response delivered before association: %q", host)
		}
		if !bytes.Equal(data, answerPacket) {
			t.Error("inbound DNS/TCP payload was changed")
		}
		seenAnswer = true
	}}
	if err := i.handlePacket(queryPacket, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	if err := i.handlePacket(answerPacket, packetMetadata{}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	if !seenQuery || !seenAnswer {
		t.Fatalf("TCP DNS request/response missed: %v / %v", seenQuery, seenAnswer)
	}
	flow := i.flowMetadata(syn, packetProcess{path: "chrome.exe"})
	if flow.Host != "first.example" || flow.DomainSource != "dns" {
		t.Fatalf("initial TCP flow lacks associated domain: %+v", flow)
	}
}
