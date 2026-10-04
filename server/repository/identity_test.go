package repository

import (
	"errors"
	"path/filepath"
	"testing"
)

func openIdentityTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestIdentityShortIDAndIndependentLogin(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithLogin("engineering", "Engineering", "admin", "hashed-password")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ShortID != "engineering" || !identity.LoginConfigured || identity.PolicyRevision != 1 {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	resolved, err := db.ResolveIdentity("ENGINEERING")
	if err != nil || resolved.IdentityID != identity.ID || resolved.IdentityName != identity.Name {
		t.Fatalf("short identity id did not resolve: %+v err=%v", resolved, err)
	}
	user, err := db.GetUserByUsername("engineering")
	if err != nil || user.IdentityID != identity.ID || user.Role != "user" {
		t.Fatalf("identity login was not created independently: %+v err=%v", user, err)
	}

	disabled := IdentityStatusDisabled
	updated, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Status: &disabled, PolicyRevision: identity.PolicyRevision})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != IdentityStatusDisabled || updated.PolicyRevision != 2 {
		t.Fatalf("unexpected updated identity: %+v", updated)
	}
	if _, err := db.ResolveIdentity(identity.ShortID); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("disabled identity resolved with err=%v", err)
	}
	user, err = db.GetUserByUsername("engineering")
	if err != nil || user.Status != IdentityStatusDisabled {
		t.Fatalf("identity login status was not synchronized: %+v err=%v", user, err)
	}
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{
		Status: &disabled, PolicyRevision: identity.PolicyRevision,
	}); !errors.Is(err, ErrIdentityRevisionConflict) {
		t.Fatalf("stale identity revision accepted: %v", err)
	}
}

func TestIdentityRevisionDisableAndDeviceAssignment(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentityWithShortID("operations", "Operations", "admin")
	if err != nil {
		t.Fatal(err)
	}
	disabled := IdentityStatusDisabled
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	device := &Device{
		ID: "dev_identity_test", Name: "Identity Test",
		Fingerprint: "identity-test-fingerprint", InstallationID: "identity-test-install",
		ApprovalState:         EnrollmentApproved,
		RequestedCapabilities: []string{"proxy.client"},
		ApprovedCapabilities:  []string{"proxy.client"},
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	summary, err := db.SetDeviceIdentity(device.ID, identity.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if summary.IdentityID != identity.ID || summary.IdentityName != identity.Name || summary.IdentityStatus != IdentityStatusDisabled {
		t.Fatalf("unexpected device identity summary: %+v", summary)
	}
	ids, err := db.ListDeviceIDsForIdentity(identity.ID)
	if err != nil || len(ids) != 1 || ids[0] != device.ID {
		t.Fatalf("unexpected identity devices: %+v err=%v", ids, err)
	}
	if _, err := db.SetDeviceIdentity(device.ID, "", "admin"); err == nil {
		t.Fatal("identity assignment was cleared even though all devices require an identity")
	}
}

func TestHistoricalDeviceMigrationReusesExplicitIdentityBinding(t *testing.T) {
	db := openIdentityTestDB(t)
	admin := &User{Username: "migration-admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	identity, err := db.CreateIdentityWithLogin("historical", "Historical Owner", admin.ID, "hash")
	if err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "historical-fingerprint", InstallationID: "historical-installation",
		PublicKey: []byte("historical-public-key"), DeviceName: "Historical Device",
		RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
	}
	pending, err := db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := db.ApproveEnrollment(pending.RequestID, admin.ID, observation.RequestedCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(historical.ID, identity.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	identityUserID, err := db.GetIdentityLoginUserID(identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	ownerUserID, err := db.GetDeviceOwnerUserID(historical.ID)
	if err != nil || ownerUserID != identityUserID {
		t.Fatalf("historical device ownership was not transferred to the identity login: owner=%q err=%v", ownerUserID, err)
	}
	if _, err := db.Exec(`UPDATE devices SET owner_user_id = ? WHERE id = ?`, admin.ID, historical.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(historical.ID, identity.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	ownerUserID, err = db.GetDeviceOwnerUserID(historical.ID)
	if err != nil || ownerUserID != identityUserID {
		t.Fatalf("same-identity repair did not transfer historical ownership: owner=%q err=%v", ownerUserID, err)
	}
	resolved, err := db.ResolveIdentity(identity.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.DeviceID != historical.ID || migrated.IdentityID != identity.ID || len(migrated.ApprovedCapabilities) != 2 {
		t.Fatalf("historical device was duplicated or changed: %+v", migrated)
	}
	var deviceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM devices WHERE public_key_fingerprint = ?`, observation.Fingerprint).Scan(&deviceCount); err != nil || deviceCount != 1 {
		t.Fatalf("migration created duplicate devices: count=%d err=%v", deviceCount, err)
	}

	other, err := db.CreateIdentityWithShortID("other-owner", "Other Owner", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherResolved, err := db.ResolveIdentity(other.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ObserveIdentityDevice(*otherResolved, observation); !errors.Is(err, ErrDeviceIdentityConflict) {
		t.Fatalf("historical device accepted another identity id: %v", err)
	}
}
