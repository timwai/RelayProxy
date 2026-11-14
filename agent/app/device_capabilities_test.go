package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func containsCapability(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestRouterDeviceCapabilitiesAreExitOnlyAndNoE2EE(t *testing.T) {
	cfg := AgentConfig{
		RouterMode: true,
		Mode: "EXIT",
		P2PMode: "auto",
		BrowserNetworkShare: true,
	}
	requested, transport := buildDeviceCapabilities(cfg, true, true)

	if len(requested) != 1 || requested[0] != protocol.CapabilityProxyExit {
		t.Fatalf("Router requested capabilities = %v, want only %q", requested, protocol.CapabilityProxyExit)
	}
	for _, forbidden := range []string{
		protocol.CapabilityProxyClient,
		protocol.CapabilityFilesClient,
		protocol.CapabilityFilesHost,
		protocol.CapabilityRDPClient,
		protocol.CapabilityRDPHost,
		protocol.CapabilityRDPPublic,
	} {
		if containsCapability(requested, forbidden) {
			t.Fatalf("Router unexpectedly requested capability %q: %v", forbidden, requested)
		}
	}
	for _, required := range []string{"tcp", "tls", protocol.UDPModeStream, protocol.CapabilityTargetACL, protocol.CapabilityExitSystemDNS} {
		if !containsCapability(transport, required) {
			t.Fatalf("Router transport capabilities missing %q: %v", required, transport)
		}
	}
	for _, forbidden := range []string{
		"quic",
		protocol.CapabilityE2EEV1,
		protocol.CapabilityProxyP2P,
		protocol.CapabilityProxyStreamResume,
		protocol.CapabilityProxyPublicDirect,
		protocol.UDPModeDatagram,
		protocol.CapabilitySpeedTest,
		protocol.CapabilityResourceInventoryPush,
		protocol.CapabilityBrowserNetworkExit,
	} {
		if containsCapability(transport, forbidden) {
			t.Fatalf("Router unexpectedly advertised transport capability %q: %v", forbidden, transport)
		}
	}
}

func TestStandardAgentCapabilitiesRemainAvailable(t *testing.T) {
	cfg := AgentConfig{Mode: "BOTH", P2PMode: "auto"}
	requested, transport := buildDeviceCapabilities(cfg, true, true)
	for _, required := range []string{
		protocol.CapabilityProxyClient,
		protocol.CapabilityProxyExit,
		protocol.CapabilityFilesClient,
		protocol.CapabilityFilesHost,
	} {
		if !containsCapability(requested, required) {
			t.Fatalf("standard Agent capabilities missing %q: %v", required, requested)
		}
	}
	for _, required := range []string{"quic", protocol.CapabilityE2EEV1, protocol.UDPModeDatagram, protocol.CapabilityProxyP2P} {
		if !containsCapability(transport, required) {
			t.Fatalf("standard Agent transport capabilities missing %q: %v", required, transport)
		}
	}
}
