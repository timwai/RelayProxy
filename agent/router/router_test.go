package router

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestCapabilitiesAreExitOnlyAndStreamUDP(t *testing.T) {
	requested, transport := Capabilities()
	if len(requested) != 1 || requested[0] != protocol.CapabilityProxyExit {
		t.Fatalf("requested capabilities = %v, want only %q", requested, protocol.CapabilityProxyExit)
	}
	want := map[string]bool{
		"tcp":                            true,
		"tls":                            true,
		protocol.UDPModeStream:           true,
		protocol.CapabilityTargetACL:     true,
		protocol.CapabilityExitSystemDNS: true,
	}
	if len(transport) != len(want) {
		t.Fatalf("transport capabilities = %v, want %d entries", transport, len(want))
	}
	for _, capability := range transport {
		if !want[capability] {
			t.Fatalf("unexpected transport capability %q", capability)
		}
		delete(want, capability)
	}
	if len(want) != 0 {
		t.Fatalf("missing transport capabilities: %v", want)
	}
}

func TestRouterProfileDoesNotAdvertiseOptionalServices(t *testing.T) {
	requested, transport := Capabilities()
	all := append(append([]string{}, requested...), transport...)
	for _, disabled := range []string{
		"proxy.client", "rdp.controller", "rdp.host", "files.client", "files.host",
		"browser.network.exit.v1", "proxy.p2p", "proxy.public_direct", "e2ee_v1",
		"udp_datagram_v1", "quic", "resource.inventory.push_v1",
	} {
		for _, capability := range all {
			if capability == disabled {
				t.Fatalf("Router must not advertise %q", disabled)
			}
		}
	}
}
