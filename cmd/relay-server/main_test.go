package main

import (
	"reflect"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

func TestGatewayAuthorizationIncludesOnlyApprovedRDPTargetFields(t *testing.T) {
	decision := &repository.DeviceAuthorization{
		State: "approved", DeviceID: "controller", OwnerUserID: "owner",
		ApprovedCapabilities: []string{"rdp.controller"},
		RDPTargets: []*repository.RDPTarget{
			nil,
			{DeviceID: "target", Name: "Office PC", Online: true, Service: "internal", Port: 3389},
		},
	}
	got := gatewayAuthorization(decision)
	if got.State != decision.State || got.DeviceID != decision.DeviceID || got.OwnerUserID != decision.OwnerUserID {
		t.Fatalf("gateway authorization identity mismatch: %+v", got)
	}
	if !reflect.DeepEqual(got.ApprovedCapabilities, decision.ApprovedCapabilities) {
		t.Fatalf("gateway authorization capabilities = %+v", got.ApprovedCapabilities)
	}
	if len(got.RDPTargets) != 1 || got.RDPTargets[0].DeviceID != "target" || got.RDPTargets[0].Name != "Office PC" || !got.RDPTargets[0].Online {
		t.Fatalf("gateway authorization RDP targets = %+v", got.RDPTargets)
	}
}

func TestRefreshRDPTargetOnlineStateUsesAuthenticatedHostSessions(t *testing.T) {
	sessions := session.NewManager()
	sessions.Register(&session.DeviceSession{DeviceID: "online-host", Grants: []string{protocol.CapabilityRDPHost}})
	sessions.Register(&session.DeviceSession{DeviceID: "online-without-host-grant"})
	targets := []protocol.RDPTarget{
		{DeviceID: "online-host"},
		{DeviceID: "online-without-host-grant", Online: true},
		{DeviceID: "offline", Online: true},
	}

	refreshRDPTargetOnlineState(targets, sessions)
	if !targets[0].Online || targets[1].Online || targets[2].Online {
		t.Fatalf("live RDP target state = %+v", targets)
	}
}
