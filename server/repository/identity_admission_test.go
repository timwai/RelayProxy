package repository

import (
	"database/sql"
	"errors"
	"testing"
)

func TestIdentityEnrollmentRequiresApprovalAndRechecksIdentity(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveIdentity(identity.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	status := IdentityStatusDisabled
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Status: &status}); err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
		RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
	}
	if _, err := db.ObserveIdentityDevice(*resolved, observation); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("stale identity resolution admitted after disable: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled enrollment left devices: %d %v", count, err)
	}
}

func TestIdentityEnrollmentIsScopedAndPreservesDeviceCapabilities(t *testing.T) {
	db := openIdentityTestDB(t)
	owner, err := db.CreateIdentityWithLogin("owner-two", "Before", "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateIdentity("Other", "admin")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveIdentity(owner.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	ownerUserID, err := db.GetIdentityLoginUserID(owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
		DeviceName: "Client", RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
	}
	pending, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil || pending.State != EnrollmentPending || pending.RequestID == "" {
		t.Fatalf("first connection was not pending: %+v err=%v", pending, err)
	}
	if _, err := db.ApproveEnrollmentForIdentity(pending.RequestID, "other-admin", other.ID, []string{"proxy.client"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("another identity approved the request: %v", err)
	}
	approved, err := db.ApproveEnrollmentForIdentity(pending.RequestID, ownerUserID, owner.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := db.GetDeviceIdentitySummary(approved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.IdentityID != owner.ID || len(approved.ApprovedCapabilities) != 1 {
		t.Fatalf("unexpected approved device: %+v", approved)
	}
	after := "After"
	if _, err := db.UpdateIdentity(owner.ID, "admin", IdentityUpdate{Name: &after}); err != nil {
		t.Fatal(err)
	}
	reconnected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.IdentityName != after || reconnected.PolicyRevision != 2 || len(reconnected.ApprovedCapabilities) != 1 || reconnected.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("reconnect bypassed identity metadata or device capabilities: %+v", reconnected)
	}
}

func TestDeviceCapabilitiesControlAdmissionAndFeatures(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithLogin("feature-owner", "Owner", "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveIdentity(identity.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	reviewerID, err := db.GetIdentityLoginUserID(identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
		RequestedCapabilities: []string{"proxy.client", "rdp.controller"},
	}
	pending, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	client, err := db.ApproveEnrollmentForIdentity(pending.RequestID, reviewerID, identity.ID, observation.RequestedCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "target", "Target", identity.ID, []string{"proxy.exit", "rdp.host"})
	if _, err := db.UpdateDeviceCapabilities(client.ID, reviewerID, []string{"proxy.client"}); err != nil {
		t.Fatal(err)
	}
	if !db.IsIdentityDeviceAuthorized("fp", client.ID, identity.ID) {
		t.Fatal("device capability change unexpectedly invalidated identity authentication")
	}
	managed, allowed, err := db.authorizeIdentityDeviceFeature(client.ID, "target", GrantFeatureProxyUse)
	if err != nil || !managed || !allowed {
		t.Fatalf("approved proxy capability was not honored: %v %v %v", managed, allowed, err)
	}
	managed, allowed, err = db.authorizeIdentityDeviceFeature(client.ID, "target", GrantFeatureRDPConnect)
	if err != nil || !managed || allowed {
		t.Fatalf("removed RDP device capability remained effective: %v %v %v", managed, allowed, err)
	}
	reconnected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconnected.ApprovedCapabilities) != 1 || reconnected.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("reconnect self-expanded device capabilities: %+v", reconnected.ApprovedCapabilities)
	}
}
