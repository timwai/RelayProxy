package divert

import (
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeDirectRejectsDNSRebindingAndReservedAddresses(t *testing.T) {
	for _, addr := range []string{
		"127.0.0.1", "::1", "0.0.0.1", "10.0.0.2", "172.20.0.1",
		"192.168.1.1", "169.254.169.254", "100.100.100.200",
		"192.0.2.5", "198.18.0.100", "198.51.100.1",
		"203.0.113.10", "224.0.0.1", "2001:db8::1234", "fc00::1",
	} {
		if _, err := safeFakeDirectAddress(addr); err == nil {
			t.Errorf("reserved/rebinding destination %s was accepted", addr)
		}
	}
	for _, addr := range []string{"9.9.9.9", "8.8.4.4", "2001:4860:4860::8888"} {
		if _, err := safeFakeDirectAddress(addr); err != nil {
			t.Errorf("public destination %s was refused: %v", addr, err)
		}
	}
}

func TestFakeDirectRechecksCurrentRoutingWithOriginalProcessAndRealIP(t *testing.T) {
	rechecked := 0
	active := true
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionDirect},
		FakeIPEnabled: func() bool { return active },
		SharedPolicy: func(f Flow) Decision {
			if f.IP != "" {
				rechecked++
				if f.Process != "allowed.exe" || f.Host != "allowed.example" || f.Port != 443 {
					t.Errorf("real-IP recheck lost original connection identity: %+v", f)
				}
				if f.IP == "8.8.8.8" {
					return Decision{Action: ActionReject, Rule: "priority-ip-deny"}
				}
				if f.IP == "8.8.4.4" {
					return Decision{Action: ActionProxy, Rule: "priority-ip-proxy"}
				}
			}
			return Decision{Action: ActionDirect, Rule: "allow-direct"}
		},
		Guard: LoopGuard{RelayIPs: []string{"1.1.1.1"}, RelayPorts: []int{443}},
	})
	fake, ok := s.fakeDNS.allocate("allowed.example", dnsmessage.TypeA)
	if !ok {
		t.Fatal("allocate FakeIP failed")
	}
	f := testFlow(ProtoTCP, nil)
	f.IP, f.Host, f.Process, f.Port = fake.String(), "spoofed.example", "allowed.exe", 443
	route, err := s.ClassifyFlow(f)
	if err != nil {
		t.Fatal(err)
	}
	if route.Metadata().Host != "allowed.example" || route.Decision().Action != ActionDirect {
		t.Fatalf("bad initial FakeIP route: %+v", route)
	}
	for _, test := range []struct {
		ip     string
		reject bool
	}{
		{"9.9.9.9", false},
		{"8.8.8.8", true},
		{"8.8.4.4", true},
		{"127.0.0.1", true},
		{"1.1.1.1", true}, // relay control connection on same port
	} {
		err := s.validateFakeDirectTarget(route, test.ip)
		if (err != nil) != test.reject {
			t.Errorf("%s: rejected=%v, expected=%v: %v", test.ip, err != nil, test.reject, err)
		}
	}
	if rechecked < 3 {
		t.Fatalf("not enough real-IP policy checks: %d", rechecked)
	}
	active = false
	if err := s.validateFakeDirectTarget(route, "9.9.9.9"); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("active FakeIP policy transition was ignored: %v", err)
	}
}
