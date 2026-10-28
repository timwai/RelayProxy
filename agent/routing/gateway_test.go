package routing

import (
	"context"
	"net"
	"net/netip"
	"relayproxy/internal/proxy"
	"testing"
)

func TestGatewaySourceAndEntryAreConjunctive(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{Name: "LAN TV", Enabled: true, Entries: []string{"gateway"}, SourceCIDRs: []string{"192.168.2.0/24"}, Targets: []string{"*.example.com"}, Action: ActionProxy, ExitID: "tv-exit"}}}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	f := Flow{Entry: "gateway", SourceIP: "192.168.2.3", Host: "media.example.com", Port: 443, Protocol: "tcp"}
	if d := e.DecideFlow(f); d.ExitID != "tv-exit" {
		t.Fatal(d)
	}
	for _, changed := range []Flow{{Entry: "socks5", SourceIP: f.SourceIP, Host: f.Host}, {Entry: f.Entry, SourceIP: "192.168.3.3", Host: f.Host}, {Entry: f.Entry, Host: f.Host}} {
		if d := e.DecideFlow(changed); d.Action != ActionReject {
			t.Fatal(d)
		}
	}
	cfg.Rules[0].SourceCIDRs = []string{"nonsense"}
	if ValidateConfig(cfg) == nil {
		t.Fatal("invalid source accepted")
	}
}

func TestGatewayDirectUsesObservedIPInsteadOfSystemDNS(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, e := listener.Accept()
		if e == nil {
			c.Close()
		}
	}()
	target := listener.Addr().(*net.TCPAddr).AddrPort()
	source := netip.MustParseAddrPort("10.1.2.3:1234")
	ctx := proxy.WithGatewayFlow(context.Background(), source, target, "lan0")
	engine, err := NewEngine(Config{Mode: ModeDirect, DNSMode: DNSModeLocal})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	d := NewRoutingDialer(engine, nil)
	c, err := d.DialTCP(ctx, "", "does-not-exist.invalid", target.Port())
	if err != nil {
		t.Fatal("DIRECT should use packet destination", err)
	}
	c.Close()
	resolved, err := d.ResolveProxyTarget(ctx, "does-not-exist.invalid")
	if err != nil || resolved != "127.0.0.1" {
		t.Fatal("local DNS override lost observed IP", resolved, err)
	}
}
