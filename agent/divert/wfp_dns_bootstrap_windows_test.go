//go:build windows

package divert

import "testing"

func TestWFPDNSProxyBootstrap(t *testing.T) {
	tests := []struct {
		name           string
		protocol       Protocol
		port           uint16
		mode           DNSMode
		proxyReady     bool
		proxyEverReady bool
		want           bool
	}{
		{"udp dns before first ready", ProtoUDP, 53, DNSModeProxy, false, false, true},
		{"tcp dns before first ready", ProtoTCP, 53, DNSModeProxy, false, false, true},
		{"udp dns while ready", ProtoUDP, 53, DNSModeProxy, true, false, false},
		{"udp dns after first ready disconnect", ProtoUDP, 53, DNSModeProxy, false, true, false},
		{"auto is not forced proxy bootstrap", ProtoUDP, 53, DNSModeAuto, false, false, false},
		{"non dns port", ProtoUDP, 443, DNSModeProxy, false, false, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wfpDNSProxyBootstrap(tc.protocol, tc.port, tc.mode, tc.proxyReady, tc.proxyEverReady); got != tc.want {
				t.Fatalf("wfpDNSProxyBootstrap()=%v want=%v", got, tc.want)
			}
		})
	}
}
