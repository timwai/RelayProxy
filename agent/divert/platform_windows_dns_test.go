//go:build windows

package divert

import (
	"strings"
	"testing"
)

func TestWindowsFilterCapturesDNSOnRelayIP(t *testing.T) {
	filter := windowsInterceptFilter(45001, 45002, LoopGuard{RelayIPs: []string{"192.0.2.11", "2001:db8::11"}})
	if !strings.Contains(filter, "ip.DstAddr != 192.0.2.11") ||
		!strings.Contains(filter, "ipv6.DstAddr != 2001:db8::11") {
		t.Fatalf("Windows filter dropped Relay transport bypass: %s", filter)
	}
	for _, part := range []string{
		"outbound and !loopback and ((udp and udp.DstPort == 53) or (tcp and tcp.DstPort == 53))",
		"inbound and !loopback and ((udp and udp.SrcPort == 53) or (tcp and tcp.SrcPort == 53))",
		"tcp.DstPort == 45001", "tcp.DstPort == 45002",
	} {
		if !strings.Contains(filter, part) {
			t.Fatalf("Windows filter lost DNS or TCP reflection rule %q: %s", part, filter)
		}
	}
}

func TestWindowsDNSCacheRefreshOnlyForObservationalMode(t *testing.T) {
	cases := []struct {
		name                        string
		associate, fakeIP, proxyDNS bool
		want                        bool
	}{
		{"real IP association", true, false, false, true},
		{"disabled association", false, false, false, false},
		{"FakeIP active", true, true, false, false},
		{"proxy DNS active", true, false, true, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRefreshWindowsSystemDNSCache(tt.associate, tt.fakeIP, tt.proxyDNS); got != tt.want {
				t.Fatalf("refresh cache = %v, want %v", got, tt.want)
			}
		})
	}
}
