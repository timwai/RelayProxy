package divert

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"relayproxy/agent/routing"
	"relayproxy/internal/traffic"
)

func TestInboundPassThroughAndDirectTCPTraffic(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		t.Run(map[bool]string{false: "ipv4", true: "ipv6"}[ipv6], func(t *testing.T) {
			stats := traffic.NewRegistry(0, 0)
			i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}, Traffic: stats})
			syn := interceptedSYN(ipv6)
			original, _ := parseIPPacket(syn)
			if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
				t.Fatal(err)
			}
			expectInterceptedPacket(t, device)
			send := func(payload string, outbound bool, flags byte) {
				data, off := packetTestFixture(ipv6, ProtoTCP, []byte(payload), false)
				data[off+13] = flags
				from, to := original.Source, original.Destination
				if !outbound {
					from, to = to, from
				}
				if err := rewriteIPPacket(data, from, to); err != nil {
					t.Fatal(err)
				}
				want := append([]byte(nil), data...)
				if err := i.handlePacket(data, packetMetadata{outbound: outbound}); err != nil {
					t.Fatal(err)
				}
				got := expectInterceptedPacket(t, device)
				if !bytes.Equal(got.data, want) || got.meta.outbound != outbound {
					t.Fatal("DIRECT packet changed")
				}
			}
			send("hello", true, 0x18)
			send("welcome", false, 0x18)
			s := stats.Snapshot()
			if s.Active != 1 || s.Upload != 5 || s.Download != 7 || s.Connections[0].Accounting != "packet" {
				t.Fatalf("DIRECT counters: %+v", s)
			}
			send("", true, 0x11)
			if stats.Snapshot().Active != 1 {
				t.Fatal("TCP half-close finalized early")
			}
			send("", false, 0x11)
			if stats.Snapshot().Active != 0 {
				t.Fatal("completed TCP still active")
			}
		})
	}
}

func TestInboundUDPAndUnrelatedPacketsPassThrough(t *testing.T) {
	stats := traffic.NewRegistry(0, 0)
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}, Traffic: stats})
	data, _ := packetTestFixture(false, ProtoUDP, []byte("request"), false)
	p, _ := parseIPPacket(data)
	if err := i.handlePacket(data, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	response, err := makeUDPReply(FlowKey{Protocol: ProtoUDP, Source: p.Source, Destination: p.Destination}, []byte("response"))
	if err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(response, packetMetadata{}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	s := stats.Snapshot()
	if s.Upload != 7 || s.Download != 8 || s.Active != 1 {
		t.Fatalf("UDP direct counters: %+v", s)
	}
	unrelated := interceptedSYN(false)
	if err := i.handlePacket(unrelated, packetMetadata{}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	fragment := []byte{0x45, 0, 0, 20, 0, 0, 0x20, 0, 64, 17, 0, 0, 192, 0, 2, 1, 198, 51, 100, 1}
	if err := i.handlePacket(fragment, packetMetadata{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectInterceptedPacket(t, device).data, fragment) {
		t.Fatal("normal inbound fragment was altered")
	}
	i.server.Close()
	if stats.Snapshot().Active != 0 {
		t.Fatal("UDP record outlived server")
	}
}

func TestDNSAssociatedDomainFeedsSharedCompoundRouting(t *testing.T) {
	engine, err := routing.NewEngine(routing.Config{Mode: routing.ModeRule, DefaultAction: routing.ActionReject, Rules: []routing.Rule{
		{Enabled: true, Ports: []string{"53"}, Action: routing.ActionDirect},
		{Name: "web", Enabled: true, Processes: []string{"browser.exe"}, Targets: []string{"*.example.test"}, Ports: []string{"443"}, Protocols: []string{"tcp"}, Action: routing.ActionProxy, ExitID: "shared-exit"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	stats := traffic.NewRegistry(0, 0)
	i, device := newTestInterceptor(t, Options{Traffic: stats, SharedPolicy: func(f Flow) Decision {
		d := engine.DecideFlow(routing.Flow{Process: f.Process, Host: f.Host, IP: f.IP, Port: f.Port, Protocol: string(f.Protocol)})
		return Decision{Action: Action(d.Action), ExitID: d.ExitID, Rule: d.Rule}
	}})
	syn := interceptedSYN(false)
	target, _ := parseIPPacket(syn)
	q, r := dnsExchange(t, "api.example.test", target.Destination.Addr(), 60)
	query, _ := packetTestFixture(false, ProtoUDP, q, false)
	resolver := netip.MustParseAddrPort("192.0.2.53:53")
	if err := rewriteIPPacket(query, target.Source, resolver); err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(query, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	response, err := makeUDPReply(FlowKey{Protocol: ProtoUDP, Source: target.Source, Destination: resolver}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.handlePacket(response, packetMetadata{}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	reflected := expectInterceptedPacket(t, device)
	if reflected.meta.outbound {
		t.Fatal("shared compound proxy rule did not intercept TCP")
	}
	row := stats.Snapshot().Connections[0]
	if row.Rule != "web" || row.ExitID != "shared-exit" || row.Host != "api.example.test" || row.DomainSource != "dns" {
		t.Fatalf("missing classified metadata: %+v", row)
	}
}

func TestIdleDirectConnectionRemainsWhileOSOwnsIt(t *testing.T) {
	stats := traffic.NewRegistry(0, 0)
	i, device := newTestInterceptor(t, Options{Config: Config{DefaultAction: ActionDirect}, Traffic: stats})
	syn := interceptedSYN(false)
	p, _ := parseIPPacket(syn)
	if err := i.handlePacket(syn, packetMetadata{outbound: true}); err != nil {
		t.Fatal(err)
	}
	expectInterceptedPacket(t, device)
	key := FlowKey{Protocol: ProtoTCP, Source: p.Source, Destination: p.Destination}
	now := time.Now()
	i.tcp[key].lastSeen = now.Add(-3 * time.Minute)
	i.sweepDirectTCP(now)
	if stats.Snapshot().Active != 1 {
		t.Fatal("quiet owned socket expired")
	}
	i.lookup = func(Protocol, netip.AddrPort, netip.AddrPort) (packetProcess, error) {
		return packetProcess{pid: 99, path: "different.exe"}, nil
	}
	i.sweepDirectTCP(now)
	if stats.Snapshot().Active != 0 {
		t.Fatal("reused socket kept stale process telemetry")
	}
}
