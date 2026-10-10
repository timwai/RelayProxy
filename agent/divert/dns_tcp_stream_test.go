package divert

import (
	"net/netip"
	"testing"
)

func dnsTestTCPPacket(from, to netip.AddrPort, sequence uint32, payload []byte) ipPacket {
	return ipPacket{
		Protocol: ProtoTCP,
		Source:   from, Destination: to,
		TCPSequence: sequence,
		Payload:     payload,
	}
}

func TestPassiveDNSTCPReassemblesSplitQuestionAndAnswer(t *testing.T) {
	d := newDNSAssociations()
	client := netip.MustParseAddrPort("192.0.2.10:54122")
	server := netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.25")
	q, a := dnsExchange(t, "first.example", ip, 90)
	query := dnsTCPWire(q)
	reply := dnsTCPWire(a)
	// Splitting even the DNS/TCP two-byte length field is legitimate.
	d.observeTCPQuery(dnsTestTCPPacket(client, server, 100, query[:1]))
	d.observeTCPQuery(dnsTestTCPPacket(client, server, 101, query[1:]))
	if host := d.lookup(ip); host != "" {
		t.Fatalf("query alone assigned DNS hostname %q", host)
	}
	cut := len(reply) / 2
	d.observeTCPResponse(dnsTestTCPPacket(server, client, 500, reply[:cut]))
	if host := d.lookup(ip); host != "" {
		t.Fatalf("partial response assigned DNS hostname %q", host)
	}
	d.observeTCPResponse(dnsTestTCPPacket(server, client, 500+uint32(cut), reply[cut:]))
	if host := d.lookup(ip); host != "first.example" {
		t.Fatalf("completed DNS/TCP response not associated: %q", host)
	}
}

func TestPassiveDNSTCPRejectsSequenceGapsAndOverlaps(t *testing.T) {
	client := netip.MustParseAddrPort("192.0.2.10:54123")
	server := netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.26")
	q, a := dnsExchange(t, "gap.example", ip, 60)
	for _, tt := range []struct {
		name string
		seq  uint32
	}{
		{"gap", 1000 + uint32(len(dnsTCPWire(a))/2) + 2},
		{"partial overlap", 1000 + uint32(len(dnsTCPWire(a))/2) - 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := newDNSAssociations()
			d.observeTCPQuery(dnsTestTCPPacket(client, server, 200, dnsTCPWire(q)))
			reply := dnsTCPWire(a)
			cut := len(reply) / 2
			d.observeTCPResponse(dnsTestTCPPacket(server, client, 1000, reply[:cut]))
			d.observeTCPResponse(dnsTestTCPPacket(server, client, tt.seq, reply[cut:]))
			if host := d.lookup(ip); host != "" {
				t.Fatalf("out-of-order DNS data attributed %q", host)
			}
		})
	}
}

func TestPassiveDNSTCPRetransmissionAndMultipleMessages(t *testing.T) {
	d := newDNSAssociations()
	client := netip.MustParseAddrPort("192.0.2.10:54124")
	server := netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.27")
	q, a := dnsExchange(t, "retry.example", ip, 70)
	wire := dnsTCPWire(q)
	d.observeTCPQuery(dnsTestTCPPacket(client, server, 400, wire))
	d.observeTCPQuery(dnsTestTCPPacket(client, server, 400, wire)) // retransmission
	d.observeTCPResponse(dnsTestTCPPacket(server, client, 900, dnsTCPWire(a)))
	if host := d.lookup(ip); host != "retry.example" {
		t.Fatalf("retransmission destroyed pending DNS association: %q", host)
	}
}

func TestPassiveDNSTCPBufferCapacityAndInvalidLengths(t *testing.T) {
	o := newDNSTCPObserver()
	source := netip.MustParseAddrPort("192.0.2.10:54125")
	destination := netip.MustParseAddrPort("192.0.2.53:53")
	for n := 0; n < dnsTCPMaxStreams+15; n++ {
		client := netip.AddrPortFrom(source.Addr(), uint16(20000+n))
		_ = o.observe(dnsTestTCPPacket(client, destination, 10, []byte{0}))
	}
	if got := len(o.streams); got > dnsTCPMaxStreams {
		t.Fatalf("unbounded passive DNS TCP streams: %d", got)
	}
	bad := dnsTestTCPPacket(source, destination, 500, []byte{0, 1, 0})
	if messages := o.observe(bad); len(messages) != 0 {
		t.Fatal("invalid DNS TCP length was accepted")
	}
}
