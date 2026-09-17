package divert

import (
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func dnsExchange(t *testing.T, name string, ip netip.Addr, ttl uint32, aliases ...string) ([]byte, []byte) {
	t.Helper()
	kind := dnsmessage.TypeA
	if ip.Is6() {
		kind = dnsmessage.TypeAAAA
	}
	n := dnsmessage.MustNewName(name + ".")
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 123, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: n, Type: kind, Class: dnsmessage.ClassINET}}}
	query, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	q.Response = true
	for _, alias := range aliases {
		next := dnsmessage.MustNewName(alias + ".")
		q.Answers = append(q.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: n, Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: ttl}, Body: &dnsmessage.CNAMEResource{CNAME: next}})
		n = next
	}
	var body dnsmessage.ResourceBody = &dnsmessage.AResource{}
	if ip.Is4() {
		body = &dnsmessage.AResource{A: ip.As4()}
	} else {
		body = &dnsmessage.AAAAResource{AAAA: ip.As16()}
	}
	q.Answers = append(q.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: n, Type: kind, Class: dnsmessage.ClassINET, TTL: ttl}, Body: body})
	reply, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return query, reply
}

func TestDNSAssociationRequiresObservedMatchingExchange(t *testing.T) {
	cache := newDNSAssociations()
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	client, server := netip.MustParseAddrPort("192.0.2.1:55000"), netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.9")
	q, r := dnsExchange(t, "app.example", ip, 60, "cdn.example")
	cache.response(server, client, r)
	if cache.lookup(ip) != "" {
		t.Fatal("unsolicited DNS answer was trusted")
	}
	cache.query(client, server, q)
	cache.response(netip.MustParseAddrPort("192.0.2.54:53"), client, r)
	if cache.lookup(ip) != "" {
		t.Fatal("wrong resolver accepted")
	}
	_, other := dnsExchange(t, "other.example", ip, 60)
	cache.response(server, client, other)
	if cache.lookup(ip) != "" {
		t.Fatal("mismatched question accepted")
	}
	cache.response(server, client, r)
	if cache.lookup(ip) != "app.example" {
		t.Fatal("CNAME association lost")
	}
	now = now.Add(60 * time.Second)
	if cache.lookup(ip) != "" {
		t.Fatal("expired DNS association remained")
	}
}

func TestDNSAssociationAmbiguityTTLAndIPv6(t *testing.T) {
	cache := newDNSAssociations()
	now := time.Unix(1000, 0)
	cache.now = func() time.Time { return now }
	client, server := netip.MustParseAddrPort("[2001:db8::1]:55000"), netip.MustParseAddrPort("[2001:db8::53]:53")
	ip := netip.MustParseAddr("2001:db8:1::8")
	exchange := func(name string, ttl uint32) {
		q, r := dnsExchange(t, name, ip, ttl)
		cache.query(client, server, q)
		cache.response(server, client, r)
	}
	exchange("first.example", 120)
	if cache.lookup(ip) != "first.example" {
		t.Fatal("IPv6 result missing")
	}
	exchange("second.example", 30)
	if cache.lookup(ip) != "" {
		t.Fatal("ambiguous shared IP assigned an arbitrary hostname")
	}
	now = now.Add(31 * time.Second)
	if cache.lookup(ip) != "first.example" {
		t.Fatal("expired competing name did not clear")
	}
	now = now.Add(100 * time.Second)
	exchange("zero.example", 0)
	if cache.lookup(ip) != "" {
		t.Fatal("zero TTL answer was cached")
	}
	q, r := dnsExchange(t, "late.example", ip, 60)
	cache.query(client, server, q)
	now = now.Add(11 * time.Second)
	cache.response(server, client, r)
	if cache.lookup(ip) != "" {
		t.Fatal("late answer to expired query accepted")
	}
}

func TestDNSCNAMECyclesAndCapacity(t *testing.T) {
	cache := newDNSAssociations()
	client, server := netip.MustParseAddrPort("192.0.2.1:55000"), netip.MustParseAddrPort("192.0.2.53:53")
	ip := netip.MustParseAddr("203.0.113.1")
	q, r := dnsExchange(t, "loop.example", ip, 60, "alias.example", "loop.example")
	cache.query(client, server, q)
	cache.response(server, client, r)
	if cache.lookup(ip) != "" {
		t.Fatal("CNAME cycle accepted")
	}
	for n := 0; n < dnsCapacity+10; n++ {
		cache.query(netip.AddrPortFrom(client.Addr(), uint16(1024+n)), server, q)
	}
	if len(cache.pending) > dnsCapacity {
		t.Fatal("unbounded DNS query cache")
	}
	cache.mu.Lock()
	for n := 0; n < dnsCapacity+10; n++ {
		cache.remember(netip.AddrFrom4([4]byte{198, 18, byte(n >> 8), byte(n)}), "bounded.example", time.Now().Add(time.Minute))
	}
	cache.mu.Unlock()
	if len(cache.addresses) > dnsCapacity {
		t.Fatal("unbounded address cache")
	}
}
