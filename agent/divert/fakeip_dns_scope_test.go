package divert

import (
	"context"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPMappingIsolatedAcrossDNSExitScopes(t *testing.T) {
	d := newFakeIPDNS()
	first, ok := d.allocateScoped("example.com", dnsmessage.TypeA, "identity-one/dns-exit-a", true)
	if !ok {
		t.Fatal("first scoped mapping unavailable")
	}
	second, ok := d.allocateScoped("example.com", dnsmessage.TypeA, "identity-one/dns-exit-b", true)
	if !ok || second == first {
		t.Fatalf("distinct resolver contexts shared FakeIP: %s / %s", first, second)
	}
	again, ok := d.allocateScoped("example.com", dnsmessage.TypeA, "identity-one/dns-exit-a", true)
	if !ok || again != first {
		t.Fatalf("same resolver context mapping changed: %s -> %s", first, again)
	}
	if host, scope, scoped, ok := d.lookupWithScope(first); !ok || !scoped ||
		host != "example.com" || scope != "identity-one/dns-exit-a" {
		t.Fatalf("unexpected association provenance: %s / %s, scoped=%v, valid=%v", host, scope, scoped, ok)
	}
	// This low-level check deliberately does not claim per-process isolation:
	// queries from a Windows system DNS service may serve several apps.
}

func TestChangingDNSExitRejectsCachedFakeIPFromOldExit(t *testing.T) {
	currentExit := "exit-a"
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return true },
		DNSExitID:     func() string { return currentExit },
	})
	query := fakeDNSQuestion(t, "example.com", dnsmessage.TypeA)
	firstResponse := fakeDNSAnswer(t, s.replyFakeDNS(context.Background(), query))
	firstIP := netip.AddrFrom4(firstResponse.Answers[0].Body.(*dnsmessage.AResource).A)
	flow := testFlow(ProtoTCP, nil)
	flow.IP = firstIP.String()
	r, err := s.ClassifyFlow(flow)
	if err != nil || r.Decision().Action != ActionProxy {
		t.Fatalf("first DNS exit was not classified: %+v %v", r, err)
	}
	currentExit = "exit-b"
	r, err = s.ClassifyFlow(flow)
	if err != nil || r.Decision().Action != ActionReject || r.Decision().Rule != "fakeip-dns-exit-changed" {
		t.Fatalf("cached mapping from old DNS egress was reused: %+v %v", r, err)
	}
	secondResponse := fakeDNSAnswer(t, s.replyFakeDNS(context.Background(), query))
	secondIP := netip.AddrFrom4(secondResponse.Answers[0].Body.(*dnsmessage.AResource).A)
	if secondIP == firstIP {
		t.Fatal("new DNS exit returned the previous egress context's FakeIP")
	}
	flow.IP = secondIP.String()
	r, err = s.ClassifyFlow(flow)
	if err != nil || r.Decision().Action != ActionProxy {
		t.Fatalf("new egress FakeIP did not route: %+v %v", r, err)
	}
}
