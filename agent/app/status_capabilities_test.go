package app

import "testing"

func TestStatusApprovedCapabilitiesReturnsDefensiveCopy(t *testing.T) {
	agent := &Agent{approvedCapabilities: []string{"proxy.client", "rdp.controller"}}
	status := agent.Status()
	if len(status.ApprovedCapabilities) != 2 {
		t.Fatalf("approved capabilities = %#v", status.ApprovedCapabilities)
	}
	status.ApprovedCapabilities[0] = "mutated"
	again := agent.Status()
	if again.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("status caller mutated agent capabilities: %#v", again.ApprovedCapabilities)
	}
}

func TestStatusReportsProxyPaused(t *testing.T) {
	agent := &Agent{}
	agent.SetProxyPaused(true)
	if !agent.Status().ProxyPaused {
		t.Fatal("proxyPaused status did not reflect paused runtime state")
	}
	agent.SetProxyPaused(false)
	if agent.Status().ProxyPaused {
		t.Fatal("proxyPaused status did not clear after resume")
	}
}
