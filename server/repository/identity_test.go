package repository

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
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

func TestIdentityAccessKeyLifecycle(t *testing.T) {
	db := openIdentityTestDB(t)

	identity, err := db.CreateIdentity("Engineering", "admin", []string{
		"proxy.client", "proxy.exit", "rdp.controller", "rdp.host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Status != IdentityStatusActive || identity.PolicyRevision != 1 {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	expires := time.Now().UTC().Add(time.Hour)
	issued, err := db.IssueIdentityAccessKey(identity.ID, "admin", "laptop rollout", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if issued.AccessKey == "" || issued.AccessKey[:len(IdentityAccessKeyPrefix)] != IdentityAccessKeyPrefix {
		t.Fatalf("unexpected issued access key: %+v", issued)
	}

	authorization, err := db.ResolveIdentityAccessKey(issued.AccessKey)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.IdentityID != identity.ID || authorization.KeyID != issued.ID {
		t.Fatalf("access key resolved to wrong identity: %+v", authorization)
	}
	if authorization.KeyDigest == issued.AccessKey {
		t.Fatal("stored key digest unexpectedly equals plaintext key")
	}

	keys, err := db.ListIdentityAccessKeys(identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].ID != issued.ID || keys[0].LastUsedAt == nil {
		t.Fatalf("unexpected access key list: %+v", keys)
	}

	if err := db.RevokeIdentityAccessKey(identity.ID, issued.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	// Revocation is idempotent for an existing key.
	if err := db.RevokeIdentityAccessKey(identity.ID, issued.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveIdentityAccessKey(issued.AccessKey); !errors.Is(err, ErrInvalidIdentityAccessKey) {
		t.Fatalf("revoked key resolved with err=%v", err)
	}
}

func TestIdentityRevisionDisableAndDeviceAssignment(t *testing.T) {
	db := openIdentityTestDB(t)

	identity, err := db.CreateIdentity("Operations", "admin", []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := db.IssueIdentityAccessKey(identity.ID, "admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	disabled := IdentityStatusDisabled
	updated, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{
		Status: &disabled, PolicyRevision: identity.PolicyRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != IdentityStatusDisabled || updated.PolicyRevision != 2 {
		t.Fatalf("unexpected updated identity: %+v", updated)
	}
	if _, err := db.ResolveIdentityAccessKey(issued.AccessKey); !errors.Is(err, ErrInvalidIdentityAccessKey) {
		t.Fatalf("disabled identity key resolved with err=%v", err)
	}
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{
		Status: &disabled, PolicyRevision: identity.PolicyRevision,
	}); !errors.Is(err, ErrIdentityRevisionConflict) {
		t.Fatalf("stale identity revision accepted: %v", err)
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
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != device.ID {
		t.Fatalf("unexpected identity devices: %+v", ids)
	}

	cleared, err := db.SetDeviceIdentity(device.ID, "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.IdentityID != "" || cleared.IdentityName != "" {
		t.Fatalf("identity assignment was not cleared: %+v", cleared)
	}
}
