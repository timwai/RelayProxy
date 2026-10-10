package divert

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func dnsTCPWire(payload []byte) []byte {
	wire := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(wire[:2], uint16(len(payload)))
	copy(wire[2:], payload)
	return wire
}

func TestPassiveDNSTCPAssociationBeforeFirstConnection(t *testing.T) {
	cache := newDNSAssociations()
	client := netip.MustParseAddrPort("192.0.2.10:51000")
	resolver := netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.10")
	q, reply := dnsExchange(t, "video.example", ip, 60)
	cache.queryTCP(client, resolver, dnsTCPWire(q))
	if host := cache.lookup(ip); host != "" {
		t.Fatalf("a query alone must not attribute the IP: %q", host)
	}
	// UDP and TCP may have identical question IDs and endpoint tuples. A UDP
	// response must not satisfy a TCP DNS question that was actually observed.
	cache.response(resolver, client, reply)
	if host := cache.lookup(ip); host != "" {
		t.Fatalf("mismatched transport attributed %q", host)
	}
	cache.responseTCP(resolver, client, dnsTCPWire(reply))
	if host := cache.lookup(ip); host != "video.example" {
		t.Fatalf("TCP answer was not available before the first connection: %q", host)
	}
}

func TestPassiveDNSTCPRejectsUnpairedPartialAndWrongResolver(t *testing.T) {
	client := netip.MustParseAddrPort("192.0.2.10:51001")
	server := netip.MustParseAddrPort("192.0.2.53:53")
	other := netip.MustParseAddrPort("192.0.2.54:53")
	ip := netip.MustParseAddr("203.0.113.11")
	query, reply := dnsExchange(t, "untrusted.example", ip, 60)
	for _, scenario := range []struct {
		name string
		observe func(*dnsAssociations)
	}{
		{"unsolicited", func(d *dnsAssociations) { d.responseTCP(server, client, dnsTCPWire(reply)) }},
		{"partial question", func(d *dnsAssociations) {
			wire := dnsTCPWire(query)
			d.queryTCP(client, server, wire[:len(wire)-1])
			d.responseTCP(server, client, dnsTCPWire(reply))
		}},
		{"partial answer", func(d *dnsAssociations) {
			d.queryTCP(client, server, dnsTCPWire(query))
			wire := dnsTCPWire(reply)
			d.responseTCP(server, client, wire[:len(wire)-1])
		}},
		{"wrong resolver", func(d *dnsAssociations) {
			d.queryTCP(client, server, dnsTCPWire(query))
			d.responseTCP(other, client, dnsTCPWire(reply))
		}},
		{"wrong port", func(d *dnsAssociations) {
			d.queryTCP(client, netip.MustParseAddrPort("192.0.2.53:853"), dnsTCPWire(query))
			d.responseTCP(server, client, dnsTCPWire(reply))
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cache := newDNSAssociations()
			scenario.observe(cache)
			if host := cache.lookup(ip); host != "" {
				t.Fatalf("untrusted TCP DNS data attributed %q", host)
			}
		})
	}
}
