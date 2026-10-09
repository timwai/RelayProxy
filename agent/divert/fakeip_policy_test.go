package divert

import (
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestFakeIPNeverFallsThroughToDirectOrUnavailableRelay(t *testing.T) {
	for _, tc := range []struct {
		name          string
		defaultAction Action
		ready         bool
		wantRule      string
	}{
		{"direct", ActionDirect, true, "fakeip-direct-unsupported"},
		{"relay-down", ActionProxy, false, "fakeip-proxy-unavailable"},
		{"proxy", ActionProxy, true, "global-fake-proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(t, Options{
				Config:        Config{DefaultAction: tc.defaultAction},
				FakeIPEnabled: func() bool { return true },
				SharedPolicy: func(flow Flow) Decision {
					if flow.IP != "" {
						t.Errorf("FakeIP address was passed into IP/CIDR rule engine: %s", flow.IP)
					}
					if flow.Host != "play.google.com" {
						t.Errorf("FakeIP domain was not recovered before routing: %s", flow.Host)
					}
					return Decision{Action: tc.defaultAction, Rule: "global-fake-proxy"}
				},
				ProxyReady: func() bool { return tc.ready },
			})
			fake, ok := s.fakeDNS.allocate("play.google.com", dnsmessage.TypeA)
			if !ok {
				t.Fatal("failed to allocate fake IP")
			}
			flow := testFlow(ProtoTCP, nil)
			flow.IP = fake.String()
			route, err := s.ClassifyFlow(flow)
			if err != nil {
				t.Fatal(err)
			}
			if route.Decision().Rule != tc.wantRule {
				t.Fatalf("wrong decision: %+v", route.Decision())
			}
			if tc.name != "proxy" && route.Decision().Action != ActionReject {
				t.Fatalf("fake destination escaped as DIRECT: %+v", route.Decision())
			}
			if tc.name == "proxy" && proxyDialTarget(route.Metadata()) != "play.google.com" {
				t.Fatal("PROXY attempted to send a FakeIP address")
			}
		})
	}
}

func TestFakeIPMappingIsRejectedWhenFeatureSwitchedOff(t *testing.T) {
	enabled := true
	s := newTestServer(t, Options{
		Config:        Config{DefaultAction: ActionProxy},
		FakeIPEnabled: func() bool { return enabled },
	})
	ip, ok := s.fakeDNS.allocate("example.com", dnsmessage.TypeA)
	if !ok {
		t.Fatal("allocation failed")
	}
	enabled = false
	flow := testFlow(ProtoTCP, nil)
	flow.IP = ip.String()
	route, err := s.ClassifyFlow(flow)
	if err != nil {
		t.Fatal(err)
	}
	if route.Decision().Action != ActionReject || route.Decision().Rule != "fakeip-disabled" {
		t.Fatalf("stale FakeIP escaped after disable: %+v", route.Decision())
	}
	if !isFakeIP(netip.MustParseAddr(flow.IP)) {
		t.Fatal("test did not use a FakeIP target")
	}
}
