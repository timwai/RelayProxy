package main

import (
	"reflect"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

type fakeProxyExitInventoryStore struct {
	devices      []*repository.Device
	ownerDevices map[string][]*repository.Device
	authorized   map[string]bool
	identities   map[string]*repository.DeviceIdentitySummary
}

func (f *fakeProxyExitInventoryStore) ListDevices() ([]*repository.Device, error) {
	return append([]*repository.Device(nil), f.devices...), nil
}

func (f *fakeProxyExitInventoryStore) ListDevicesForOwner(owner string) ([]*repository.Device, error) {
	return append([]*repository.Device(nil), f.ownerDevices[owner]...), nil
}

func (f *fakeProxyExitInventoryStore) AuthorizeClientExit(clientID, exitID string) (bool, error) {
	return f.authorized[clientID+"|"+exitID], nil
}

func (f *fakeProxyExitInventoryStore) GetDeviceIdentitySummary(deviceID string) (*repository.DeviceIdentitySummary, error) {
	if item := f.identities[deviceID]; item != nil {
		copy := *item
		return &copy, nil
	}
	return &repository.DeviceIdentitySummary{DeviceID: deviceID}, nil
}

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

func TestListAuthorizedProxyExitInventoryKeepsOfflineAuthorizedExits(t *testing.T) {
	store := &fakeProxyExitInventoryStore{
		devices: []*repository.Device{
			{ID: "client", Name: "Client", ApprovalState: "approved", ApprovedCapabilities: []string{protocol.CapabilityProxyClient}},
			{ID: "exit-online", Name: "Online DB", ApprovalState: "approved", ApprovedCapabilities: []string{protocol.CapabilityProxyExit}},
			{ID: "exit-offline", Name: "Offline Exit", ApprovalState: "approved", ApprovedCapabilities: []string{protocol.CapabilityProxyExit}},
			{ID: "exit-denied", Name: "Denied", ApprovalState: "approved", ApprovedCapabilities: []string{protocol.CapabilityProxyExit}},
			{ID: "exit-revoked", Name: "Revoked", ApprovalState: "revoked", ApprovedCapabilities: []string{protocol.CapabilityProxyExit}},
		},
		authorized: map[string]bool{
			"client|exit-online":  true,
			"client|exit-offline": true,
			"client|exit-denied":  false,
		},
		identities: map[string]*repository.DeviceIdentitySummary{
			"exit-online": {
				DeviceID: "exit-online", IdentityID: "identity-client", IdentityName: "Client Identity",
			},
			"exit-offline": {
				DeviceID: "exit-offline", IdentityID: "identity-other", IdentityName: "Other Identity",
			},
		},
	}
	sessions := session.NewManager()
	sessions.Register(&session.DeviceSession{
		DeviceID: "exit-online", DeviceName: "Online Live", IdentityID: "identity-client",
		IdentityName: "Client Identity", Grants: []string{protocol.CapabilityProxyExit},
	})

	got, err := listAuthorizedProxyExitInventory(store, sessions, "client", "", "identity-client")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("inventory=%+v, want two authorized exits", got)
	}
	byID := make(map[string]protocol.ProxyExit, len(got))
	for _, item := range got {
		byID[item.DeviceID] = item
	}
	online := byID["exit-online"]
	if !online.Online || online.Name != "Online Live" ||
		online.AuthorizationSource != "same_identity" || online.IdentityName != "Client Identity" {
		t.Fatalf("online exit=%+v", online)
	}
	offline := byID["exit-offline"]
	if offline.Online || offline.Name != "Offline Exit" ||
		offline.AuthorizationSource != "explicit" || offline.IdentityName != "Other Identity" {
		t.Fatalf("offline exit=%+v", offline)
	}
	if _, ok := byID["exit-denied"]; ok {
		t.Fatal("unauthorized exit was included")
	}
	if _, ok := byID["exit-revoked"]; ok {
		t.Fatal("revoked exit was included")
	}
}

func TestListAuthorizedProxyExitInventoryLegacyOwnerKeepsOfflineExit(t *testing.T) {
	store := &fakeProxyExitInventoryStore{
		ownerDevices: map[string][]*repository.Device{
			"owner-a": {
				{ID: "exit-offline", OwnerUserID: "owner-a", Name: "Legacy Exit",
					ApprovalState: "approved", ApprovedCapabilities: []string{protocol.CapabilityProxyExit}},
			},
		},
	}
	got, err := listAuthorizedProxyExitInventory(store, session.NewManager(), "client", "owner-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "exit-offline" || got[0].Online ||
		got[0].AuthorizationSource != "legacy" {
		t.Fatalf("legacy offline inventory=%+v", got)
	}
}
