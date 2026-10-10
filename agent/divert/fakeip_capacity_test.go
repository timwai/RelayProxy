package divert

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPCapacityReclaimsExpiredWithoutRecyclingAddresses(t *testing.T) {
	d := newFakeIPDNS()
	now := time.Unix(1700000000, 0)
	d.now = func() time.Time { return now }
	for i := range fakeIPLimit {
		if _, ok := d.allocate(fmt.Sprintf("site-%d.example", i), dnsmessage.TypeA); !ok {
			t.Fatalf("FakeIP allocation failed before reaching capacity: %d", i)
		}
	}
	if _, ok := d.allocate("overflow.example", dnsmessage.TypeA); ok {
		t.Fatal("full live FakeIP table accepted another domain")
	}
	original, ok := d.lookup(netip.MustParseAddr("198.18.0.1"))
	if !ok || original != "site-0.example" {
		t.Fatalf("original mapping lost: %q %v", original, ok)
	}
	now = now.Add(16 * time.Minute)
	replacement, ok := d.allocate("new.example", dnsmessage.TypeA)
	if !ok {
		t.Fatal("expired FakeIP table did not reclaim capacity")
	}
	if replacement == netip.MustParseAddr("198.18.0.1") {
		t.Fatal("expired FakeIP was reassigned to another domain")
	}
	if len(d.byIP) != 1 || len(d.byName) != 1 {
		t.Fatalf("expired associations not removed: ips=%d names=%d", len(d.byIP), len(d.byName))
	}
	if _, ok := d.lookup(netip.MustParseAddr("198.18.0.1")); ok {
		t.Fatal("previously issued address unexpectedly resolves to a new host")
	}
	if !d.wasIssued(netip.MustParseAddr("198.18.0.1")) {
		t.Fatal("issued tombstone disappeared; disabling FakeIP could leak a cached address")
	}
}

func TestFakeIPOffDoesNotBlockUnrelatedReservedAddresses(t *testing.T) {
	s := newTestServer(t, Options{Config: Config{DefaultAction: ActionProxy}})
	for _, target := range []string{"198.18.20.30", "2001:db8:198:18::1234"} {
		addr := netip.MustParseAddr(target)
		if !isFakeIP(addr) {
			t.Fatalf("bad test address %s", target)
		}
		if s.fakeIPDestination(addr) {
			t.Fatalf("never-issued reserved address was blocked with FakeIP off: %s", target)
		}
		flow := testFlow(ProtoTCP, nil)
		flow.IP = target
		route, err := s.ClassifyFlow(flow)
		if err != nil {
			t.Fatal(err)
		}
		if route.Decision().Action != ActionProxy {
			t.Fatalf("ordinary reserved-range connection unexpectedly blocked: %+v", route.Decision())
		}
	}
}

func TestFakeIPDestinationNeverTakesLoopbackSourceBypass(t *testing.T) {
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return true },
	})
	ip, ok := s.fakeDNS.allocate("play.google.com", dnsmessage.TypeA)
	if !ok {
		t.Fatal("FakeIP allocation failed")
	}
	flow := testFlow(ProtoTCP, nil)
	flow.SourceIP = "127.0.0.1"
	flow.IP = ip.String()
	route, err := s.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision().Action != ActionProxy || route.Metadata().Host != "play.google.com" {
		t.Fatalf("loopback source incorrectly bypassed FakeIP routing: %+v", route.Decision())
	}
}

func TestFakeIPOffPacketInterceptionPreservesReservedRangeTraffic(t *testing.T) {
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}})
	syn := interceptedSYN(false)
	original, err := parseIPPacket(syn)
	if err != nil {
		t.Fatal(err)
	}
	if err := rewriteIPPacket(syn, original.Source, netip.MustParseAddrPort("198.19.200.100:443")); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	sent := expectInterceptedPacket(t, device)
	if !sent.meta.outbound {
		t.Fatal("FakeIP-off mode diverted an unrelated reserved-range destination")
	}
	packet, err := parseIPPacket(sent.data)
	if err != nil || packet.Destination != netip.MustParseAddrPort("198.19.200.100:443") {
		t.Fatalf("FakeIP-off mode rewrote a real destination: %+v %v", packet, err)
	}
}
