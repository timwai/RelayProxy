package routing

import (
	"context"
	"errors"
	"net"
	"testing"

	"relayproxy/internal/proxy"
)

func TestDNSFollowsDomainRouteWithoutGuessingProcessOrPort(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionDirect, ProxyDNSEnabled: true, DNSFollowRouting: true, Rules: []Rule{
		{Name: "app", Enabled: true, Action: ActionProxy, ExitID: "app-exit", Processes: []string{"browser"}, DNSResolver: "exit_system"},
		{Name: "https", Enabled: true, Action: ActionProxy, ExitID: "https-exit", Ports: []string{"443"}},
		{Name: "tcp", Enabled: true, Action: ActionProxy, ExitID: "tcp-exit", Protocols: []string{"tcp"}},
		{Name: "domain", Enabled: true, Action: ActionProxy, ExitID: "domain-exit", Targets: []string{"example.test"}, Protocols: []string{"tcp", "udp"}},
		{Name: "blocked", Enabled: true, Action: ActionReject, Targets: []string{"blocked.test"}},
	}}
	e, err := newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := e.DNSPolicy("Example.Test.")
	if p.Rule != "domain" || p.ExitID != "domain-exit" || p.Resolver != "encrypted" {
		t.Fatalf("DNS route: %+v", p)
	}
	if p = e.DNSPolicy("other.test"); p.Action != DNSActionLocal || p.Resolver != "local" {
		t.Fatalf("DIRECT DNS: %+v", p)
	}
	if !e.DNSBlocked("blocked.test") {
		t.Fatal("REJECT did not refuse DNS")
	}
	d := e.DecideFlow(Flow{Process: "browser", Host: "example.test", Port: 443, Protocol: "tcp"})
	if d.Rule != "app" || d.ExitID != "app-exit" || d.DNSResolver != "exit_system" {
		t.Fatalf("connection lost process DNS: %+v", d)
	}
}

func TestDNSOverridesKeepDNSAndTrafficExitsSeparate(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionProxy, DNSFollowRouting: true, DNSRules: []DNSRule{
		{Name: "DNS override", Enabled: true, Action: DNSActionRemote, Domains: []string{"example.test"}, ExitID: "dns-exit", Resolver: "exit_system"},
	}, Rules: []Rule{{Name: "traffic", Enabled: true, Action: ActionProxy, ExitID: "traffic-exit"}}}
	e, err := newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	d := e.DecideFlow(Flow{Host: "example.test", Protocol: "tcp", Port: 443})
	if d.ExitID != "traffic-exit" || d.DNSExitID != "dns-exit" || d.DNSResolver != "exit_system" {
		t.Fatalf("DNS changed traffic exit: %+v", d)
	}
	cfg.DNSRules[0].ExitID = ""
	e, err = newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.DNSPolicy("example.test"); p.ExitID != "traffic-exit" {
		t.Fatalf("unselected DNS exit did not follow traffic: %+v", p)
	}
	cfg.DNSRules[0].Action = DNSActionLocal
	cfg.DNSRules[0].Resolver = ""
	e, err = newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.DNSPolicy("example.test"); p.Resolver != "local" || p.ExitID != "" {
		t.Fatalf("LOCAL override: %+v", p)
	}
}

type dnsTargetDialer struct {
	host, exit string
	calls      int
}

var errDNSTargetRecorded = errors.New("target recorded")

func (d *dnsTargetDialer) DialTCP(_ context.Context, exit, host string, _ uint16) (net.Conn, error) {
	d.host, d.exit = host, exit
	d.calls++
	return nil, errDNSTargetRecorded
}
func (d *dnsTargetDialer) DialUDP(_ context.Context, exit, host string, _ uint16) (net.PacketConn, error) {
	d.host, d.exit = host, exit
	d.calls++
	return nil, errDNSTargetRecorded
}

func TestStaticDNSRetainsDomainRoutingAndSendsIPForTCPAndUDP(t *testing.T) {
	for _, perRule := range []bool{false, true} {
		cfg := Config{Mode: ModeRule, DefaultAction: ActionReject, DNSHosts: []DNSHostMapping{{Domain: "example.test", IP: "203.0.113.80"}}, Rules: []Rule{
			{Name: "domain", Enabled: true, Action: ActionProxy, ExitID: "chosen", Targets: []string{"example.test"}},
		}}
		want := "203.0.113.80"
		if perRule {
			cfg.Rules[0].DNSResolver = "static"
			cfg.Rules[0].DNSIP = "2001:db8::80"
			want = "2001:db8::80"
		}
		e, err := newEngineSnapshot(cfg)
		if err != nil {
			t.Fatal(err)
		}
		underlying := &dnsTargetDialer{}
		dialer := NewRoutingDialer(e, underlying)
		_, err = dialer.DialTCP(context.Background(), "wrong", "example.test", 443)
		if !errors.Is(err, errDNSTargetRecorded) || underlying.host != want || underlying.exit != "chosen" {
			t.Fatalf("TCP perRule=%v target=%+v err=%v", perRule, underlying, err)
		}
		_, err = dialer.DialUDP(context.Background(), "wrong", "example.test", 443)
		if !errors.Is(err, errDNSTargetRecorded) || underlying.host != want || underlying.exit != "chosen" {
			t.Fatalf("UDP perRule=%v target=%+v err=%v", perRule, underlying, err)
		}
	}
}

func TestConnectionDNSUsesActualProcessRuleAndExit(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionProxy, DNSFollowRouting: true, Rules: []Rule{
		{Enabled: true, Action: ActionProxy, ExitID: "app-exit", Processes: []string{"browser"}, DNSResolver: "exit_system"},
		{Enabled: true, Action: ActionProxy, ExitID: "domain-exit", Targets: []string{"example.test"}, DNSResolver: "static", DNSIP: "203.0.113.10"},
	}}
	e, err := newEngineSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	underlying := &dnsTargetDialer{}
	dialer := NewRoutingDialer(e, underlying)
	dialer.ResolveDNS = func(_ context.Context, exit, host, resolver string) (string, error) {
		if exit != "app-exit" || host != "example.test" || resolver != "exit_system" {
			t.Fatalf("resolver context %s %s %s", exit, host, resolver)
		}
		return "203.0.113.20", nil
	}
	ctx := proxy.WithClientProcess(context.Background(), "browser", nil)
	_, err = dialer.DialTCP(ctx, "default", "example.test", 443)
	if !errors.Is(err, errDNSTargetRecorded) || underlying.host != "203.0.113.20" || underlying.exit != "app-exit" {
		t.Fatalf("process rule lost: %+v %v", underlying, err)
	}
	dialer.ResolveDNS = func(context.Context, string, string, string) (string, error) {
		return "", errors.New("DNS unavailable")
	}
	_, err = dialer.DialUDP(ctx, "default", "example.test", 443)
	if err == nil || underlying.calls != 1 {
		t.Fatal("resolver failure unexpectedly forwarded hostname")
	}
}

func TestRoutingDNSValidation(t *testing.T) {
	for _, r := range []Rule{
		{Action: ActionProxy, DNSResolver: "static"},
		{Action: ActionProxy, DNSResolver: "static", DNSIP: "example.test"},
		{Action: ActionProxy, DNSResolver: "static", DNSIP: "fe80::1%en0"},
		{Action: ActionProxy, DNSResolver: "local", DNSIP: "192.168.1.1"},
		{Action: ActionProxy, DNSResolver: "exit_system", ExitID: "local:proxy"},
		{Action: ActionReject, DNSResolver: "encrypted"},
	} {
		if err := ValidateConfig(Config{Rules: []Rule{r}}); err == nil {
			t.Fatalf("accepted invalid DNS: %+v", r)
		}
	}
}

func TestFakeIPInheritedDirectKeepsExistingRecoveryPath(t *testing.T) {
	e, err := newEngineSnapshot(Config{Mode: ModeRule, FakeIPEnabled: true, DNSFollowRouting: true, DefaultAction: ActionProxy, Rules: []Rule{
		{Name: "app direct", Enabled: true, Action: ActionDirect, Processes: []string{"browser"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if p := e.DNSPolicy("example.test"); p.Resolver != "encrypted" {
		t.Fatalf("guessed application during DNS: %+v", p)
	}
	d := e.DecideFlow(Flow{Entry: "divert", Process: "browser", Host: "example.test", Port: 443, Protocol: "tcp"})
	if d.Action != ActionDirect || d.DNSResolver != "" {
		t.Fatalf("inherited DIRECT bypassed FakeIP recovery: %+v", d)
	}
}

func TestDNSStaticMappingUsesKnownIPForCIDRRouting(t *testing.T) {
	e, err := newEngineSnapshot(Config{Mode: ModeRule, DNSFollowRouting: true, ProxyDNSEnabled: true, DefaultAction: ActionProxy,
		DNSHosts: []DNSHostMapping{{Domain: "app.example", IP: "203.0.113.20"}},
		Rules:    []Rule{{Name: "blocked subnet", Enabled: true, Action: ActionReject, Targets: []string{"203.0.113.0/24"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p := e.DNSPolicy("app.example"); p.Action != DNSActionReject || p.Rule != "blocked subnet" {
		t.Fatalf("static DNS ignored IP routing: %+v", p)
	}
	if _, ok := e.DNSHostIP("app.example"); ok {
		t.Fatal("static answer bypassed matched REJECT")
	}
}
