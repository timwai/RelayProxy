package routing

import "testing"

func TestCompoundWildcardSelectors(t *testing.T) {
	cases := []struct {
		name      string
		processes []string
		targets   []string
		matching  []Flow
		rejected  []Flow
	}{
		{
			name: "process trailing wildcard", processes: []string{"chrome*"},
			matching: []Flow{{Process: `C:\Apps\CHROME.EXE`}, {Process: "chrome-helper.exe"}},
			rejected: []Flow{{Process: "portable-chrome.exe"}, {}},
		},
		{
			name: "process leading wildcard", processes: []string{"*chrome.exe"},
			matching: []Flow{{Process: `C:\Apps\portable-chrome.exe`}, {Process: "chrome.exe"}},
			rejected: []Flow{{Process: "chrome.exe.bak"}, {}},
		},
		{
			name: "process wildcards on both ends", processes: []string{"*chrome*"},
			matching: []Flow{{Process: `C:\Apps\portable-chrome-helper.exe`}, {Process: "chrome.exe"}},
			rejected: []Flow{{Process: `C:\chrome\msedge.exe`}, {}},
		},
		{
			name: "domain leading wildcard", targets: []string{"*example.com"},
			matching: []Flow{{Host: "API.EXAMPLE.COM."}, {Host: "example.com"}},
			rejected: []Flow{{Host: "example.com.invalid"}, {IP: "192.0.2.1"}, {}},
		},
		{
			name: "domain trailing wildcard", targets: []string{"example.*"},
			matching: []Flow{{Host: "example.com"}, {Host: "example.net"}},
			rejected: []Flow{{Host: "notexample.com"}, {}},
		},
		{
			name: "domain wildcards on both ends", targets: []string{"*example*"},
			matching: []Flow{{Host: "api.example.com"}, {Host: "my-example.net"}},
			rejected: []Flow{{Host: "unrelated.test"}, {}},
		},
		{
			name: "optional subdomain with wildcard suffix", targets: []string{"*.example.*"},
			matching: []Flow{{Host: "example.com"}, {Host: "api.example.net"}},
			rejected: []Flow{{Host: "notexample.com"}, {}},
		},
		{
			name: "IPv4 trailing wildcard", targets: []string{"192.168.*"},
			matching: []Flow{{IP: "192.168.1.20"}, {Host: "192.168.1.20"}, {Host: "unrelated.test", IP: "192.168.1.20"}, {IP: "::ffff:192.168.1.20"}},
			rejected: []Flow{{IP: "192.169.1.20"}, {IP: "192.168.1.999"}, {Host: "unrelated.test"}, {}},
		},
		{
			name: "IPv4 leading wildcard", targets: []string{"*100.7"},
			matching: []Flow{{IP: "198.51.100.7"}, {IP: "203.0.100.7"}},
			rejected: []Flow{{IP: "198.51.100.70"}, {IP: "100.0.0.7"}, {}},
		},
		{
			name: "IPv4 wildcards on both ends", targets: []string{"*168.1*"},
			matching: []Flow{{IP: "192.168.1.20"}, {IP: "192.168.100.20"}},
			rejected: []Flow{{IP: "192.168.2.20"}, {}},
		},
		{
			name: "IP middle and single character wildcards", targets: []string{"192.*.1.?"},
			matching: []Flow{{IP: "192.168.1.5"}},
			rejected: []Flow{{IP: "192.168.1.50"}, {IP: "192.168.2.5"}},
		},
		{
			name: "IPv6 trailing wildcard and canonical spelling", targets: []string{"2001:DB8:*"},
			matching: []Flow{{IP: "2001:db8::1"}, {IP: "2001:0db8:0000:0000:0000:0000:0000:0001"}, {Host: "2001:db8::2"}},
			rejected: []Flow{{IP: "2001:db9::1"}, {IP: "192.0.2.1"}, {}},
		},
		{
			name: "IPv6 leading wildcard", targets: []string{"*::abcd"},
			matching: []Flow{{IP: "2001:db8::abcd"}, {IP: "::abcd"}},
			rejected: []Flow{{IP: "2001:db8::abcd:1"}, {}},
		},
		{
			name: "IPv6 wildcards on both ends", targets: []string{"*DB8*"},
			matching: []Flow{{IP: "2001:db8::1"}, {IP: "2001:db8:1::abcd"}},
			rejected: []Flow{{IP: "2001:db9::1"}, {}},
		},
		{
			name: "target wildcard checks both known domain and IP", targets: []string{"*1*"},
			matching: []Flow{{Host: "cdn1.example.test"}, {IP: "192.0.2.5"}},
			rejected: []Flow{{Host: "cdn.example.test", IP: "203.0.2.5"}, {}},
		},
		{
			name: "explicit any still includes unknown identities", processes: []string{"*"}, targets: []string{"*"},
			matching: []Flow{{}},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{
				Name: test.name, Enabled: true, Processes: test.processes, Targets: test.targets, Action: ActionDirect,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, flow := range test.matching {
				if decision := engine.DecideFlow(flow); !decision.Matched || decision.Action != ActionDirect {
					t.Errorf("expected wildcard match for %+v, got %+v", flow, decision)
				}
			}
			for _, flow := range test.rejected {
				if decision := engine.DecideFlow(flow); decision.Matched || decision.Action != ActionReject {
					t.Errorf("unexpected wildcard match for %+v: %+v", flow, decision)
				}
			}
		})
	}
}

func TestCompoundWildcardCombinationAndReload(t *testing.T) {
	cfg := Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{
		Name: "wildcard browser", Enabled: true, Processes: []string{"*chrome*", "*firefox*"},
		Targets: []string{"*example*", "192.168.*"}, Ports: []string{"443"}, Protocols: []string{"tcp"},
		Action: ActionProxy, ExitID: "chosen",
	}}}
	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	flow := Flow{Process: "portable-chrome.exe", IP: "192.168.1.20", Port: 443, Protocol: "tcp"}
	if d := engine.DecideFlow(flow); !d.Matched || d.ExitID != "chosen" {
		t.Fatalf("wildcard combination lost decision: %+v", d)
	}
	for _, change := range []func(*Flow){
		func(f *Flow) { f.Process = "other.exe" },
		func(f *Flow) { f.IP = "192.169.1.20" },
		func(f *Flow) { f.Port = 80 },
		func(f *Flow) { f.Protocol = "udp" },
	} {
		other := flow
		change(&other)
		if engine.DecideFlow(other).Matched {
			t.Errorf("wildcard bypassed another condition: %+v", other)
		}
	}
	cfg.Rules[0].Targets = []string{"*100.7"}
	if err := engine.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if engine.DecideFlow(flow).Matched {
		t.Fatal("old IP wildcard survived reload")
	}
	flow.IP = "198.51.100.7"
	if !engine.DecideFlow(flow).Matched {
		t.Fatal("new IP wildcard was not applied")
	}
}
