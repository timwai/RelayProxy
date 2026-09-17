package app

import (
	"testing"

	"relayproxy/agent/divert"
	"relayproxy/agent/routing"
)

func TestTransparentAndManualShareWildcardSelectors(t *testing.T) {
	a, err := NewAgent(AgentConfig{Mode: "CLIENT", NetworkMode: "divert", Routing: routing.Config{
		Mode: routing.ModeRule, DefaultAction: routing.ActionReject, Rules: []routing.Rule{{
			Name: "wildcard", Enabled: true, Processes: []string{"*browser*"},
			Targets: []string{"*example*", "203.0.113.*", "2001:db8:*"}, Ports: []string{"443"}, Protocols: []string{"tcp"},
			Action: routing.ActionProxy, ExitID: "exit-a",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i, target := range []struct{ host, ip string }{
		{"api.example.test", "198.51.100.8"},
		{"", "203.0.113.10"},
		{"", "2001:db8::10"},
	} {
		f := divert.Flow{Process: "portable-browser.exe", ProcessID: 424242, Host: target.host, IP: target.ip,
			SourceIP: "192.0.2.10", SourcePort: uint16(54000 + i), Port: 443, Protocol: divert.ProtoTCP}
		classified, err := a.divertSrv.ClassifyFlow(f)
		if err != nil {
			t.Fatal(err)
		}
		transparent := classified.Decision()
		manual := a.routingEngine.DecideFlow(routing.Flow{Process: f.Process, Host: f.Host, IP: f.IP, Port: f.Port, Protocol: "tcp"})
		if transparent.Action != divert.ActionProxy || transparent.Rule != "wildcard" || transparent.ExitID != "exit-a" ||
			string(transparent.Action) != string(manual.Action) || transparent.Rule != manual.Rule || transparent.ExitID != manual.ExitID {
			t.Fatalf("entries did not match the same wildcard for %+v: transparent=%+v manual=%+v", target, transparent, manual)
		}
	}
}

func TestTransparentAndManualUseOnePolicyAndFreezeLiveDecisions(t *testing.T) {
	config := AgentConfig{Mode: "CLIENT", NetworkMode: "divert", Routing: routing.Config{Mode: routing.ModeRule, DefaultAction: routing.ActionReject, Rules: []routing.Rule{{Name: "shared", Enabled: true, Processes: []string{"browser.exe"}, Targets: []string{"*.example.test"}, Ports: []string{"443"}, Protocols: []string{"tcp"}, Action: routing.ActionProxy, ExitID: "exit-a"}}}}
	a, err := NewAgent(config)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	f := divert.Flow{Process: "browser.exe", ProcessID: 424242, Host: "api.example.test", DomainSource: "dns", SourceIP: "192.0.2.10", SourcePort: 54000, IP: "203.0.113.10", Port: 443, Protocol: divert.ProtoTCP}
	classified, err := a.divertSrv.ClassifyFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	manual := a.routingEngine.DecideFlow(routing.Flow{Process: f.Process, Host: f.Host, IP: f.IP, Port: f.Port, Protocol: "tcp"})
	if string(classified.Decision().Action) != string(manual.Action) || classified.Decision().ExitID != manual.ExitID || classified.Decision().Rule != manual.Rule {
		t.Fatal("entries selected different policies")
	}
	next := a.Config()
	next.Routing.Rules[0].Action = routing.ActionDirect
	if err := a.ApplyPolicies(next.Routing, next.DivertConfig); err != nil {
		t.Fatal(err)
	}
	if classified.Decision().Action != divert.ActionProxy {
		t.Fatal("reload changed live decision")
	}
	f.SourcePort++
	updated, err := a.divertSrv.ClassifyFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Decision().Action != divert.ActionDirect {
		t.Fatal("new flow did not use shared reload")
	}
	next = a.Config()
	next.DivertConfig.ExcludeProcesses = []string{"browser.exe"}
	next.Routing.Mode = routing.ModeGlobalProxy
	if err := a.ApplyPolicies(next.Routing, next.DivertConfig); err != nil {
		t.Fatal(err)
	}
	f.SourcePort++
	excluded, err := a.divertSrv.ClassifyFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	if excluded.Decision().Action != divert.ActionDirect || excluded.Decision().Rule != "exclude" {
		t.Fatal("transparent exclusion lost precedence")
	}
}
