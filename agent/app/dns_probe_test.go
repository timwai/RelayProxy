package app

import (
	"context"
	"testing"

	"relayproxy/agent/routing"
)

func TestDNSPolicyProbeWorksWithoutSystemCaptureAndHonorsPriority(t *testing.T) {
	a, err := NewAgent(AgentConfig{Mode: "CLIENT", DefaultExitID: "chosen", Routing: routing.Config{
		Mode: routing.ModeRule, DefaultAction: routing.ActionProxy, DNSFollowRouting: true,
		DNSRules: []routing.DNSRule{{Name: "lower priority DNS", Enabled: true, Action: routing.DNSActionRemote, Domains: []string{"app.example"}, ExitID: "dns-exit"}},
		Rules:    []routing.Rule{{Name: "domain rule", Enabled: true, Action: routing.ActionProxy, ExitID: "traffic-exit", Targets: []string{"app.example"}, DNSResolver: "static", DNSIP: "203.0.113.12"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.divertSrv != nil {
		t.Fatal("DNS diagnostics unexpectedly enabled interception")
	}
	for _, index := range []int{-1, 0} {
		r, err := a.ProbeDNSRule(context.Background(), index, "app.example")
		if err != nil {
			t.Fatal(err)
		}
		if r.Rule != "domain rule" || r.Resolver != "static" || r.RoutingRule != "domain rule" || r.TrafficExitID != "traffic-exit" || r.RoutingAction != "PROXY" {
			t.Fatalf("effective priority hidden: %+v", r)
		}
		if len(r.Results) != 2 || len(r.Results[0].Answers) != 1 || r.Results[0].Answers[0] != "203.0.113.12" || len(r.Results[1].Answers) != 0 {
			t.Fatalf("wrong static results: %+v", r.Results)
		}
	}
	if _, err = a.ProbeDNSRule(context.Background(), 0, "other.example"); err == nil {
		t.Fatal("accepted domain not matching selected saved rule")
	}
	if _, err = a.ProbeDNSRule(context.Background(), 2, "app.example"); err == nil {
		t.Fatal("accepted removed rule")
	}
}

func TestExplicitRoutingDNSInheritsTrafficDefaultInsteadOfDedicatedDNSExit(t *testing.T) {
	a, err := NewAgent(AgentConfig{Mode: "CLIENT", DefaultExitID: "traffic-default", Routing: routing.Config{
		Mode: routing.ModeRule, DNSExitID: "dedicated-dns", Rules: []routing.Rule{{Enabled: true, Action: routing.ActionProxy, Targets: []string{"app.example"}, DNSResolver: "encrypted"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := a.dnsPolicyForHost("app.example")
	if p.ExitID != "traffic-default" {
		t.Fatalf("explicit routing DNS used dedicated DNS exit: %+v", p)
	}
}
