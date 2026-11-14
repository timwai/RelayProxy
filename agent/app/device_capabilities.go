package app

import (
	"relayproxy/internal/exitgroup"
	"relayproxy/internal/protocol"
)

// buildDeviceCapabilities keeps the device-advertised surface aligned with the
// runtime profile. Router advertises only the exit service and reliable TCP/TLS
// stream transport; it never advertises file, RDP, client, P2P, QUIC, or E2EE.
func buildDeviceCapabilities(cfg AgentConfig, hasExit, supportsDatagrams bool) ([]string, []string) {
	if cfg.RouterMode {
		requested := make([]string, 0, 1)
		transport := []string{"tcp", "tls", protocol.UDPModeStream}
		if hasExit {
			requested = append(requested, protocol.CapabilityProxyExit)
			transport = append(transport, protocol.CapabilityTargetACL, protocol.CapabilityExitSystemDNS)
		}
		return requested, transport
	}

	transport := []string{
		"tcp", "quic", "tls", protocol.UDPModeStream, protocol.CapabilitySpeedTest,
		protocol.CapabilityResourceInventoryPush, protocol.CapabilityE2EEV1,
	}
	if cfg.IsP2PEnabled() && cfg.P2PMode != "relay_only" {
		transport = append(transport, protocol.CapabilityProxyP2P, protocol.CapabilityProxyStreamResume)
	}
	if publicDirectAllowedForMode(cfg.P2PMode) {
		transport = append(transport, protocol.CapabilityProxyPublicDirect)
	}
	if supportsDatagrams {
		transport = append(transport, protocol.UDPModeDatagram)
	}

	requested := make([]string, 0, 6)
	if cfg.Mode == "CLIENT" || cfg.Mode == "BOTH" || cfg.Gateway.Enabled || exitgroup.IsID(cfg.ExitUpstreamID) {
		requested = append(requested, protocol.CapabilityProxyClient)
	}
	if hasExit {
		requested = append(requested, protocol.CapabilityProxyExit)
		transport = append(transport, protocol.CapabilityTargetACL, protocol.CapabilityExitSystemDNS)
		if cfg.BrowserNetworkShare {
			transport = append(transport, protocol.CapabilityBrowserNetworkExit)
		}
	}
	if cfg.IsRDPEnabled() {
		requested = append(requested, protocol.CapabilityRDPClient, protocol.CapabilityRDPHost, protocol.CapabilityRDPPublic)
	}
	requested = append(requested, protocol.CapabilityFilesClient, protocol.CapabilityFilesHost)
	return requested, transport
}
