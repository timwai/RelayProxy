package divert

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// A split DNS/TCP response cannot be trusted after only its first segment.
// Association must be populated synchronously by the final segment before
// delivering that segment to the Windows stack and its dependent first SYN.
func TestDNSTCPSplitResponseIsAssociatedBeforeDelivery(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}})
	syn, err := parseIPPacket(interceptedSYN(false))
	if err != nil {
		t.Fatal(err)
	}
	resolver := netip.MustParseAddrPort("192.0.2.53:53")
	query, answer := dnsExchange(t, "first-split.example", syn.Destination.Addr(), 60)
	sendQuery, _ := packetTestFixture(false, ProtoTCP, dnsTCPWire(query), false)
	if err := rewriteIPPacket(sendQuery, syn.Source, resolver); err != nil {
		t.Fatal(err)
	}
	wire := dnsTCPWire(answer)
	cut := len(wire) / 2
	first, _ := packetTestFixture(false, ProtoTCP, wire[:cut], false)
	second, offset := packetTestFixture(false, ProtoTCP, wire[cut:], false)
	binary.BigEndian.PutUint32(second[offset+4:], 0x12345678+uint32(cut))
	for _, packet := range [][]byte{first, second} {
		if err := rewriteIPPacket(packet, resolver, syn.Source); err != nil {
			t.Fatal(err)
		}
	}
	received := 0
	i.device = &dnsOrderDevice{testPacketDevice: device, beforeSend: func(_ []byte, meta packetMetadata) {
		if meta.outbound {
			return
		}
		received++
		host := i.dns.lookup(syn.Destination.Addr())
		if received == 1 && host != "" {
			t.Errorf("incomplete DNS TCP response attributed %q", host)
		}
		if received == 2 && host != "first-split.example" {
			t.Errorf("final DNS TCP segment delivered before association: %q", host)
		}
	}}
	if err := i.handlePacket(sendQuery, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	for _, packet := range [][]byte{first, second} {
		if err := i.handlePacket(packet, packetMetadata{}); err != nil {
			t.Fatal(err)
		}
		expectInterceptedPacket(t, device)
	}
	if received != 2 {
		t.Fatalf("expected both DNS TCP response segments, got %d", received)
	}
	if flow := i.flowMetadata(syn, packetProcess{path: "chrome.exe"}); flow.Host != "first-split.example" || flow.DomainSource != "dns" {
		t.Fatalf("first connection lost the DNS association: %+v", flow)
	}
}
