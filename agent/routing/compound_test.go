package routing

import "testing"

func TestCompoundSelectorsANDAcrossFieldsORWithinField(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{
		Name: "browser web", Enabled: true, Processes: []string{"chrome.exe", "firefox.exe"},
		Targets: []string{"*.example.com", "203.0.113.0/24", "2001:db8::/32"}, Ports: []string{"443", "8000-8002"},
		Protocols: []string{"tcp"}, Action: ActionProxy, ExitID: "chosen"}}})
	if err != nil {
		t.Fatal(err)
	}
	base := Flow{Process: `C:\Apps\CHROME.EXE`, Host: "Api.Example.Com.", IP: "198.51.100.8", Port: 443, Protocol: "tcp"}
	cases := []struct {
		name   string
		change func(*Flow)
		match  bool
	}{
		{"all conditions", func(*Flow) {}, true},
		{"other process value", func(f *Flow) { f.Process = `C:\Apps\firefox.exe` }, true},
		{"process mismatch", func(f *Flow) { f.Process = "game.exe" }, false},
		{"unknown process", func(f *Flow) { f.Process = "" }, false},
		{"CIDR alternative", func(f *Flow) { f.Host = "unrelated.test"; f.IP = "203.0.113.9" }, true},
		{"IPv6 alternative", func(f *Flow) { f.Host = ""; f.IP = "2001:db8::1" }, true},
		{"mapped IPv4", func(f *Flow) { f.Host = ""; f.IP = "::ffff:203.0.113.10" }, true},
		{"unknown domain", func(f *Flow) { f.Host = "" }, false},
		{"domain boundary", func(f *Flow) { f.Host = "notexample.com" }, false},
		{"base domain", func(f *Flow) { f.Host = "example.com" }, true},
		{"port range", func(f *Flow) { f.Port = 8002 }, true},
		{"port outside range", func(f *Flow) { f.Port = 8003 }, false},
		{"wrong protocol", func(f *Flow) { f.Protocol = "udp" }, false},
		{"unknown protocol", func(f *Flow) { f.Protocol = "" }, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			flow := base
			test.change(&flow)
			d := engine.DecideFlow(flow)
			if d.Matched != test.match {
				t.Fatalf("decision=%+v flow=%+v", d, flow)
			}
			if test.match && (d.Action != ActionProxy || d.ExitID != "chosen" || d.Rule != "browser web") {
				t.Fatalf("lost decision: %+v", d)
			}
		})
	}
}

func TestCompoundPathsAndDeepCopies(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{Enabled: true, Processes: []string{`C:\Trusted\*.exe`}, Targets: []string{"*.example.com"}, Ports: []string{"443"}, Protocols: []string{"tcp"}, Action: ActionDirect}}}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Process: `c:\trusted\browser.exe`, Host: "www.example.com", Port: 443, Protocol: "tcp"}
	if engine.DecideFlow(flow).Action != ActionDirect {
		t.Fatal("Windows path failed")
	}
	flow.Process = `C:\Other\browser.exe`
	if engine.DecideFlow(flow).Action != ActionReject {
		t.Fatal("path glob widened to basename")
	}
	for _, list := range [][]string{cfg.Rules[0].Processes, cfg.Rules[0].Targets, cfg.Rules[0].Ports, cfg.Rules[0].Protocols} {
		list[0] = "*"
	}
	exported := engine.Config()
	for _, list := range [][]string{exported.Rules[0].Processes, exported.Rules[0].Targets, exported.Rules[0].Ports, exported.Rules[0].Protocols} {
		list[0] = "*"
	}
	if engine.DecideFlow(flow).Action != ActionReject {
		t.Fatal("caller changed live selectors")
	}
}

func TestCompoundValidationFailsClosed(t *testing.T) {
	cases := []Rule{
		{Processes: []string{"["}}, {Processes: []string{""}}, {Processes: []string{"bad\x00.exe"}},
		{Targets: []string{"1.2.3.999"}}, {Targets: []string{"192.0.2.0/99"}}, {Targets: []string{"host:443"}}, {Targets: []string{"["}},
		{Targets: []string{"2001:gg::*"}}, {Targets: []string{"2001:db8:["}}, {Targets: []string{"example.*:443"}},
		{Targets: []string{"192.168.*/24"}}, {Targets: []string{"fe80::*%eth0"}},
		{Ports: []string{"0"}}, {Ports: []string{"+443"}}, {Ports: []string{"3-2"}}, {Ports: []string{"65536"}},
		{Protocols: []string{"icmp"}},
	}
	for _, rule := range cases {
		rule.Action = ActionProxy
		if _, err := NewEngine(Config{Rules: []Rule{rule}}); err == nil {
			t.Fatalf("accepted invalid disabled rule: %+v", rule)
		}
	}
}

func TestCompoundProcessAliasMatching(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{
		Name: "windows dns service", Enabled: true, Processes: []string{"service:Dnscache"}, Action: ActionProxy,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	unique := Flow{
		Process:        `C:\Windows\System32\svchost.exe`,
		ProcessAliases: []string{"service:Dnscache"},
	}
	if got := engine.DecideFlow(unique); !got.Matched || got.Action != ActionProxy {
		t.Fatalf("service alias did not match shared routing: %+v", got)
	}
	shared := Flow{Process: `C:\Windows\System32\svchost.exe`}
	if got := engine.DecideFlow(shared); got.Matched || got.Action != ActionReject {
		t.Fatalf("shared host guessed a missing service alias: %+v", got)
	}
}

func TestCompoundTargetRuleDoesNotRequireProcessIdentity(t *testing.T) {
	engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{
		Name: "SMB target", Enabled: true,
		Targets: []string{"198.51.100.20"}, Ports: []string{"445"}, Protocols: []string{"tcp"},
		Action: ActionProxy, ExitID: "lan-exit",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Process: "", IP: "198.51.100.20", Port: 445, Protocol: "tcp"}
	got := engine.DecideFlow(flow)
	if !got.Matched || got.Action != ActionProxy || got.Rule != "SMB target" || got.ExitID != "lan-exit" {
		t.Fatalf("target-only rule required a process identity: %+v", got)
	}

	withProcess := Flow{Process: "System", IP: "198.51.100.20", Port: 445, Protocol: "tcp"}
	got = engine.DecideFlow(withProcess)
	if !got.Matched || got.Action != ActionProxy {
		t.Fatalf("target-only rule changed when process metadata appeared: %+v", got)
	}
}

func TestCompoundProcessAndTargetCanMatchIndependently(t *testing.T) {
	tests := []struct {
		name  string
		rule  Rule
		flow  Flow
		match bool
	}{
		{
			name:  "process only",
			rule:  Rule{Name: "process-only", Enabled: true, Processes: []string{"chrome.exe"}, Action: ActionProxy},
			flow:  Flow{Process: `C:\Program Files\Google\Chrome\chrome.exe`, IP: "203.0.113.10", Port: 12345, Protocol: "tcp"},
			match: true,
		},
		{
			name:  "target only",
			rule:  Rule{Name: "target-only", Enabled: true, Targets: []string{"192.168.50.0/24"}, Action: ActionProxy},
			flow:  Flow{Process: "", IP: "192.168.50.20", Port: 445, Protocol: "tcp"},
			match: true,
		},
		{
			name:  "process and target",
			rule:  Rule{Name: "combined", Enabled: true, Processes: []string{"chrome.exe"}, Targets: []string{"203.0.113.10"}, Action: ActionProxy},
			flow:  Flow{Process: "chrome.exe", IP: "203.0.113.10", Port: 443, Protocol: "tcp"},
			match: true,
		},
		{
			name:  "combined process mismatch",
			rule:  Rule{Name: "combined", Enabled: true, Processes: []string{"chrome.exe"}, Targets: []string{"203.0.113.10"}, Action: ActionProxy},
			flow:  Flow{Process: "firefox.exe", IP: "203.0.113.10", Port: 443, Protocol: "tcp"},
			match: false,
		},
		{
			name:  "combined target mismatch",
			rule:  Rule{Name: "combined", Enabled: true, Processes: []string{"chrome.exe"}, Targets: []string{"203.0.113.10"}, Action: ActionProxy},
			flow:  Flow{Process: "chrome.exe", IP: "203.0.113.11", Port: 443, Protocol: "tcp"},
			match: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{test.rule}})
			if err != nil {
				t.Fatal(err)
			}
			got := engine.DecideFlow(test.flow)
			if got.Matched != test.match {
				t.Fatalf("decision=%+v flow=%+v", got, test.flow)
			}
		})
	}
}
