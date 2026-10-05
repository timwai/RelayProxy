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

func TestRemovingApprovedCapabilityDoesNotImmediatelyRequeueIt(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithLogin("capability-editor", "Capability Editor", "admin", "hash")
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
		Fingerprint: "capability-edit-fp", InstallationID: "capability-edit-install",
		PublicKey: []byte("capability-edit-public"),
		RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
	}
	pending, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollmentForIdentity(
		pending.RequestID, reviewerID, identity.ID, observation.RequestedCapabilities,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.UpdateDeviceCapabilities(device.ID, reviewerID, []string{"proxy.client"}); err != nil {
		t.Fatal(err)
	}
	reconnected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.State != EnrollmentApproved || reconnected.RequestID != "" ||
		len(reconnected.ApprovedCapabilities) != 1 || reconnected.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("administrator-removed capability was requeued on reconnect: %+v", reconnected)
	}
	requests, err := db.ListEnrollmentRequestsForIdentity(EnrollmentPending, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("administrator-removed capability appeared in pending enrollments: %+v", requests)
	}

	// Stopping the declaration clears the server-side denial. Re-enabling it
	// later is a genuine new request and must require approval again.
	observation.RequestedCapabilities = []string{"proxy.client"}
	if _, err := db.ObserveIdentityDevice(*resolved, observation); err != nil {
		t.Fatal(err)
	}
	observation.RequestedCapabilities = []string{"proxy.client", "proxy.exit"}
	requestedAgain, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if requestedAgain.State != EnrollmentApproved || requestedAgain.RequestID == "" ||
		len(requestedAgain.ApprovedCapabilities) != 1 || requestedAgain.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("re-enabled capability did not become a fresh incremental request: %+v", requestedAgain)
	}
	requests, err = db.ListEnrollmentRequestsForIdentity(EnrollmentPending, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || len(requests[0].RequestedCapabilities) != 1 ||
		requests[0].RequestedCapabilities[0] != "proxy.exit" {
		t.Fatalf("fresh capability request was not queued correctly: %+v", requests)
	}
}

func TestApprovedIdentityDeviceCanRequestAndApproveAdditionalCapabilities(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithLogin("incremental-owner", "Incremental", "admin", "hash")
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
		Fingerprint: "incremental-fp", InstallationID: "incremental-install", PublicKey: []byte("incremental-public"),
		DeviceName: "Android", Platform: "android", Arch: "arm64",
		RequestedCapabilities: []string{"proxy.exit"},
	}
	pending, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollmentForIdentity(pending.RequestID, reviewerID, identity.ID, []string{"proxy.exit"})
	if err != nil {
		t.Fatal(err)
	}

	observation.RequestedCapabilities = []string{"proxy.client"}
	waiting, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != EnrollmentPending || waiting.DeviceID != device.ID || waiting.RequestID == "" {
		t.Fatalf("new capability without a current grant was not queued: %+v", waiting)
	}

	observation.RequestedCapabilities = []string{"proxy.client", "proxy.exit"}
	connected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if connected.State != EnrollmentApproved || connected.DeviceID != device.ID || connected.RequestID != waiting.RequestID ||
		len(connected.ApprovedCapabilities) != 1 || connected.ApprovedCapabilities[0] != "proxy.exit" {
		t.Fatalf("existing capability was not preserved while requesting another: %+v", connected)
	}
	requests, err := db.ListEnrollmentRequestsForIdentity(EnrollmentPending, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != connected.RequestID ||
		len(requests[0].RequestedCapabilities) != 1 || requests[0].RequestedCapabilities[0] != "proxy.client" {
		t.Fatalf("incremental request contains the wrong capabilities: %+v", requests)
	}
	requestDeviceID, err := db.PendingEnrollmentDeviceIDForIdentity(connected.RequestID, identity.ID)
	if err != nil || requestDeviceID != device.ID {
		t.Fatalf("incremental request device = %q, %v", requestDeviceID, err)
	}
	updated, err := db.ApproveEnrollmentForIdentity(connected.RequestID, reviewerID, identity.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != device.ID || len(updated.ApprovedCapabilities) != 2 {
		t.Fatalf("incremental approval replaced the device or its previous capability: %+v", updated)
	}
	var deviceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM devices WHERE public_key_fingerprint = ?`, observation.Fingerprint).Scan(&deviceCount); err != nil {
		t.Fatal(err)
	}
	if deviceCount != 1 {
		t.Fatalf("incremental approval created %d devices", deviceCount)
	}
	reconnected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.State != EnrollmentApproved || len(reconnected.ApprovedCapabilities) != 2 || reconnected.RequestID != "" {
		t.Fatalf("approved capabilities were not effective on reconnect: %+v", reconnected)
	}
}

func TestRejectingAdditionalCapabilityKeepsDeviceApproved(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithLogin("reject-owner", "Reject", "admin", "hash")
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
		Fingerprint: "reject-fp", InstallationID: "reject-install", PublicKey: []byte("reject-public"),
		RequestedCapabilities: []string{"proxy.exit"},
	}
	pending, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollmentForIdentity(pending.RequestID, reviewerID, identity.ID, []string{"proxy.exit"})
	if err != nil {
		t.Fatal(err)
	}
	observation.RequestedCapabilities = []string{"proxy.client", "proxy.exit"}
	connected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RejectEnrollmentForIdentity(connected.RequestID, reviewerID, identity.ID, "not allowed"); err != nil {
		t.Fatal(err)
	}
	if !db.IsIdentityDeviceAuthorized(observation.Fingerprint, device.ID, identity.ID) {
		t.Fatal("rejecting an additional capability revoked the approved device")
	}
	denied, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil || denied.State != EnrollmentApproved || denied.RequestID != "" ||
		len(denied.ApprovedCapabilities) != 1 || denied.ApprovedCapabilities[0] != "proxy.exit" {
		t.Fatalf("rejected capability was immediately reopened or removed the existing grant: %+v err=%v", denied, err)
	}
	observation.RequestedCapabilities = []string{"proxy.client"}
	denied, err = db.ObserveIdentityDevice(*resolved, observation)
	if err != nil || denied.State != EnrollmentRejected {
		t.Fatalf("a rejected standalone capability did not remain rejected: %+v err=%v", denied, err)
	}
	observation.RequestedCapabilities = []string{"proxy.exit"}
	reconnected, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil || reconnected.State != EnrollmentApproved {
		t.Fatalf("device could not reconnect with its original capability: %+v err=%v", reconnected, err)
	}
}
