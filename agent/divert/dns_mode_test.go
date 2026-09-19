package divert

import (
	"testing"

	"relayproxy/internal/traffic"
)

func dnsTestFlow(sourcePort uint16, protocol Protocol) Flow {
	return Flow{
		Process: "dns-client.exe", ProcessID: 4242,
		SourceIP: "192.0.2.20", SourcePort: sourcePort,
		IP: "1.1.1.1", Port: 53, Protocol: protocol,
	}
}

func TestDNSRoutingModes(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		ready   bool
		want    Action
		wantRule string
	}{
		{name: "direct", mode: DNSModeDirect, ready: true, want: ActionDirect, wantRule: "dns-direct"},
		{name: "proxy", mode: DNSModeProxy, ready: false, want: ActionProxy, wantRule: "dns-proxy"},
		{name: "auto bootstrap", mode: DNSModeAuto, ready: false, want: ActionDirect, wantRule: "dns-bootstrap"},
		{name: "auto ready", mode: DNSModeAuto, ready: true, want: ActionProxy, wantRule: "dns-auto"},
		{name: "rule", mode: DNSModeRule, ready: true, want: ActionDirect, wantRule: "default"},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t, Options{
				Config: Config{DNSMode: tc.mode, DefaultAction: ActionDirect},
				ProxyReady: func() bool { return tc.ready },
				Traffic: traffic.NewRegistry(0, 0),
			})
			route, err := server.ClassifyFlow(dnsTestFlow(uint16(53000+index), ProtoUDP))
			if err != nil {
				t.Fatal(err)
			}
			if route.Decision().Action != tc.want || route.Decision().Rule != tc.wantRule {
				t.Fatalf("decision=%+v want action=%s rule=%s", route.Decision(), tc.want, tc.wantRule)
			}
		})
	}
}

func TestWFPAutoDNSKeepsProxyUserspaceRouteDuringBootstrap(t *testing.T) {
	server := newTestServer(t, Options{
		Config: Config{DNSMode: DNSModeAuto, DefaultAction: ActionDirect},
		ProxyReady: func() bool { return false },
		Traffic: traffic.NewRegistry(0, 0),
	})
	route, err := server.classifyFlow(dnsTestFlow(54000, ProtoUDP), true)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision().Action != ActionProxy || route.Decision().Rule != "dns-auto" {
		t.Fatalf("WFP AUTO route=%+v", route.Decision())
	}
}

func TestInvalidDNSModeRejected(t *testing.T) {
	if err := ValidateConfig(Config{DNSMode: "sometimes"}); err == nil {
		t.Fatal("invalid DNS mode accepted")
	}
}
