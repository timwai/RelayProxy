package divert

import "testing"

func TestEngineProcessFirstMatch(t *testing.T) {
	e := requireEngine(t, Config{
		DefaultAction: ActionDirect,
		Rules: []Rule{
			{Name: "chrome", Enabled: true, Process: "chrome.exe", Action: ActionProxy},
			{Name: "all", Enabled: true, Process: "*", Action: ActionDirect},
		},
	})
	d := e.Match(Flow{Process: `C:\Program Files\Google\Chrome\chrome.exe`, IP: "1.2.3.4", Port: 443, Protocol: ProtoTCP})
	if d.Action != ActionProxy || d.Rule != "chrome" {
		t.Fatalf("got %+v", d)
	}
}

func TestEngineExcludeForcesDirect(t *testing.T) {
	e := requireEngine(t, Config{
		DefaultAction:    ActionProxy,
		ExcludeProcesses: []string{"relayproxy.exe"},
		Rules: []Rule{
			{Name: "all", Enabled: true, Process: "*", Action: ActionProxy},
		},
	})
	d := e.Match(Flow{Process: "relayproxy.exe", IP: "1.1.1.1", Port: 443, Protocol: ProtoTCP})
	if d.Action != ActionDirect || d.Rule != "exclude" {
		t.Fatalf("got %+v", d)
	}
}

func TestEnginePortAndProtoFilter(t *testing.T) {
	e := requireEngine(t, Config{
		DefaultAction: ActionDirect,
		Rules: []Rule{
			{Name: "https", Enabled: true, Process: "*", Ports: []string{"443"}, Protocols: []string{"tcp"}, Action: ActionProxy},
		},
	})
	if d := e.Match(Flow{Process: "a.exe", Port: 443, Protocol: ProtoTCP}); d.Action != ActionProxy {
		t.Fatalf("tcp/443: %+v", d)
	}
	if d := e.Match(Flow{Process: "a.exe", Port: 443, Protocol: ProtoUDP}); d.Action != ActionDirect {
		t.Fatalf("udp/443 should miss: %+v", d)
	}
	if d := e.Match(Flow{Process: "a.exe", Port: 80, Protocol: ProtoTCP}); d.Action != ActionDirect {
		t.Fatalf("tcp/80 should miss: %+v", d)
	}
}

func TestEngineCIDRAndHost(t *testing.T) {
	e := requireEngine(t, Config{
		DefaultAction: ActionDirect,
		Rules: []Rule{
			{Name: "lan", Enabled: true, Process: "*", CIDRs: []string{"10.0.0.0/8"}, Action: ActionDirect},
			{Name: "example", Enabled: true, Process: "*", Hosts: []string{"*.example.com"}, Action: ActionProxy},
		},
	})
	if d := e.Match(Flow{Process: "x", IP: "10.1.2.3", Port: 80, Protocol: ProtoTCP}); d.Rule != "lan" {
		t.Fatalf("lan: %+v", d)
	}
	if d := e.Match(Flow{Process: "x", Host: "www.example.com", IP: "1.2.3.4", Port: 80, Protocol: ProtoTCP}); d.Action != ActionProxy {
		t.Fatalf("host: %+v", d)
	}
}

func TestEngineDisabledSkipped(t *testing.T) {
	e := requireEngine(t, Config{
		DefaultAction: ActionReject,
		Rules: []Rule{
			{Name: "off", Enabled: false, Process: "*", Action: ActionProxy},
		},
	})
	if d := e.Match(Flow{Process: "x", Port: 1, Protocol: ProtoTCP}); d.Action != ActionReject {
		t.Fatalf("got %+v", d)
	}
}

func requireEngine(t *testing.T, cfg Config) *Engine {
	t.Helper()
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestEngineMatchesSafeProcessAliases(t *testing.T) {
	engine, err := NewEngine(Config{
		DefaultAction: ActionReject,
		Rules: []Rule{
			{Name: "dns service", Enabled: true, Process: "service:Dnscache", Action: ActionProxy},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	unique := Flow{
		Process:        `C:\Windows\System32\svchost.exe`,
		ProcessAliases: []string{"service:Dnscache"},
		Services:       []string{"Dnscache"},
	}
	if got := engine.Match(unique); got.Action != ActionProxy || got.Rule != "dns service" {
		t.Fatalf("unique service alias did not match: %+v", got)
	}
	shared := Flow{
		Process:  `C:\Windows\System32\svchost.exe`,
		Services: []string{"Dnscache", "NlaSvc"},
	}
	if got := engine.Match(shared); got.Action != ActionReject {
		t.Fatalf("shared service host guessed a service identity: %+v", got)
	}
}

func TestEngineProcessAndAddressConditionsAreIndependent(t *testing.T) {
	processOnly := requireEngine(t, Config{
		DefaultAction: ActionReject,
		Rules:         []Rule{{Name: "process-only", Enabled: true, Process: "chrome.exe", Action: ActionProxy}},
	})
	if got := processOnly.Match(Flow{Process: "chrome.exe", IP: "198.51.100.10", Port: 80, Protocol: ProtoTCP}); got.Action != ActionProxy {
		t.Fatalf("process-only rule missed: %+v", got)
	}

	addressOnly := requireEngine(t, Config{
		DefaultAction: ActionReject,
		Rules:         []Rule{{Name: "address-only", Enabled: true, CIDRs: []string{"192.168.50.0/24"}, Action: ActionProxy}},
	})
	if got := addressOnly.Match(Flow{Process: "", IP: "192.168.50.20", Port: 445, Protocol: ProtoTCP}); got.Action != ActionProxy {
		t.Fatalf("address-only rule required process identity: %+v", got)
	}

	combined := requireEngine(t, Config{
		DefaultAction: ActionReject,
		Rules:         []Rule{{Name: "combined", Enabled: true, Process: "chrome.exe", CIDRs: []string{"203.0.113.10"}, Action: ActionProxy}},
	})
	if got := combined.Match(Flow{Process: "chrome.exe", IP: "203.0.113.10", Port: 443, Protocol: ProtoTCP}); got.Action != ActionProxy {
		t.Fatalf("combined rule missed: %+v", got)
	}
	if got := combined.Match(Flow{Process: "firefox.exe", IP: "203.0.113.10", Port: 443, Protocol: ProtoTCP}); got.Action != ActionReject {
		t.Fatalf("combined rule ignored process condition: %+v", got)
	}
	if got := combined.Match(Flow{Process: "chrome.exe", IP: "203.0.113.11", Port: 443, Protocol: ProtoTCP}); got.Action != ActionReject {
		t.Fatalf("combined rule ignored address condition: %+v", got)
	}
}

func TestDivertUnnamedMatchingRuleGetsMonitorLabel(t *testing.T) {
	e := requireEngine(t, Config{DefaultAction: ActionReject, Rules: []Rule{
		{Name: "", Enabled: false, Process: "*", Action: ActionReject},
		{Name: "", Enabled: true, Process: "chrome.exe", Action: ActionProxy},
	}})
	got := e.Match(Flow{Process: "CHROME.EXE", Port: 443, Protocol: ProtoTCP})
	if got.Rule != "rule #2" || got.Action != ActionProxy {
		t.Fatalf("unnamed divert rule has no stable telemetry label: %+v", got)
	}
}
