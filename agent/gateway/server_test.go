package gateway

import (
	"golang.org/x/net/dns/dnsmessage"
	"net/netip"
	"testing"
	"time"
)

func dnsAnswer(name, ip string) []byte {
	n := dnsmessage.MustNewName(name + ".")
	a := netip.MustParseAddr(ip).As4()
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 123, Response: true}, Questions: []dnsmessage.Question{{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}, Answers: []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: n, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 30}, Body: &dnsmessage.AResource{A: a}}}}
	b, _ := m.Pack()
	return b
}
func TestDNSAssociationIsSourceScopedAndExpires(t *testing.T) {
	s := &Server{names: make(map[[2]netip.Addr][]association)}
	src := netip.MustParseAddr("10.0.0.2")
	dest := netip.MustParseAddr("198.18.0.2")
	k := flowKey{netip.AddrPortFrom(src, 1234), netip.AddrPortFrom(dest, 443)}
	s.remember(src, dnsAnswer("one.example", dest.String()))
	if s.host(k) != "one.example" {
		t.Fatal("DNS attribution missing")
	}
	other := k
	other.source = netip.MustParseAddrPort("10.0.0.3:1234")
	if s.host(other) != dest.String() {
		t.Fatal("DNS association crossed clients")
	}
	s.remember(src, dnsAnswer("two.example", dest.String()))
	if s.host(k) != dest.String() {
		t.Fatal("ambiguous shared address attributed")
	}
	s.names[[2]netip.Addr{src, dest}] = []association{{"expired.example", time.Now().Add(-time.Second)}}
	if s.host(k) != dest.String() {
		t.Fatal("expired association used")
	}
}
func TestGatewayConfigRejectsUnboundedOrInvalidCapture(t *testing.T) {
	for _, c := range []Config{{Enabled: true, Interface: "lo", SourceCIDRs: []string{"10.0.0.0/24"}}, {Enabled: true, Interface: "eth0"}, {Enabled: true, Interface: "eth0", SourceCIDRs: []string{"0.0.0.0/0"}}, {Enabled: true, Interface: "eth0", SourceCIDRs: []string{"::/64"}}, {Enabled: true, Interface: "eth0;evil", SourceCIDRs: []string{"10.0.0.0/24"}}} {
		if Validate(c) == nil {
			t.Fatal(c)
		}
	}
}
