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
