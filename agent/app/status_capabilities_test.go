package app

import "testing"

func TestStatusReturnsApprovedCapabilitiesCopy(t *testing.T) {
	a := &Agent{
		approvedMode:         "BOTH",
		approvedCapabilities: []string{"proxy.client", "proxy.exit", "rdp.controller"},
	}
	st := a.Status()
	if st.Mode != "BOTH" || len(st.ApprovedCapabilities) != 3 {
		t.Fatalf("unexpected status capabilities: mode=%q caps=%v", st.Mode, st.ApprovedCapabilities)
	}
	st.ApprovedCapabilities[0] = "mutated"
	if a.approvedCapabilities[0] != "proxy.client" {
		t.Fatal("status leaked the Agent capability slice")
	}
}
