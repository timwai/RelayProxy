package repository

import (
	"errors"
	"testing"
	"time"
)

func TestServerExitIdentityGrantLifecycleAndAuthorization(t *testing.T) {
	db := openIdentityTestDB(t)

	identity, err := db.CreateIdentity("Server Exit Clients", "admin", []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "server-exit-client", "Server Exit Client", identity.ID, []string{"proxy.client"})

	if allowed, err := db.AuthorizeServerExit("server-exit-client"); err != nil || allowed {
		t.Fatalf("server exit was available before explicit v4 grant: allowed=%v err=%v", allowed, err)
	}

	grant, err := db.CreateSystemIdentityGrant(
		SystemResourceServerExit, identity.ID, "admin", []string{GrantFeatureProxyUse}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ResourceID != SystemResourceServerExit || grant.GranteeIdentityID != identity.ID || grant.Revision != 1 {
		t.Fatalf("unexpected server exit grant: %+v", grant)
	}
	if allowed, err := db.AuthorizeServerExit("server-exit-client"); err != nil || !allowed {
		t.Fatalf("server exit grant not applied: allowed=%v err=%v", allowed, err)
	}
	if allowed, err := db.AuthorizeClientExit("server-exit-client", SystemResourceServerExit); err != nil || !allowed {
		t.Fatalf("generic exit authorization did not recognize server resource: allowed=%v err=%v", allowed, err)
	}

	if _, err := db.CreateSystemIdentityGrant(
		SystemResourceServerExit, identity.ID, "admin", []string{GrantFeatureProxyUse}, nil,
	); !errors.Is(err, ErrSystemIdentityGrantExists) {
		t.Fatalf("duplicate server exit grant accepted: %v", err)
	}
	if _, err := db.CreateSystemIdentityGrant(
		SystemResourceServerExit, identity.ID, "admin", []string{GrantFeatureRDPConnect}, nil,
	); err == nil {
		t.Fatal("server exit accepted RDP feature")
	}

	expires := time.Now().UTC().Add(time.Hour)
	features := []string{GrantFeatureProxyUse}
	updated, err := db.UpdateSystemIdentityGrant(grant.ID, "admin", SystemIdentityGrantUpdate{
		Features: &features, ExpiresAt: &expires, Revision: grant.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.ExpiresAt == nil {
		t.Fatalf("unexpected updated system grant: %+v", updated)
	}
	if _, err := db.UpdateSystemIdentityGrant(grant.ID, "admin", SystemIdentityGrantUpdate{
		Features: &features, Revision: 1,
	}); !errors.Is(err, ErrSystemIdentityGrantRevision) {
		t.Fatalf("stale system grant update accepted: %v", err)
	}

	if _, err := db.Exec(`UPDATE system_identity_grants SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute), grant.ID); err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.AuthorizeServerExit("server-exit-client"); err != nil || allowed {
		t.Fatalf("expired server exit grant still authorized: allowed=%v err=%v", allowed, err)
	}

	deleted, err := db.DeleteSystemIdentityGrant(grant.ID, "admin", updated.Revision)
	if err != nil || !deleted {
		t.Fatalf("delete system grant failed: deleted=%v err=%v", deleted, err)
	}
	deleted, err = db.DeleteSystemIdentityGrant(grant.ID, "admin", updated.Revision)
	if err != nil || deleted {
		t.Fatalf("idempotent system grant delete failed: deleted=%v err=%v", deleted, err)
	}
}

func TestServerExitLegacyClientRemainsCompatible(t *testing.T) {
	db := openIdentityTestDB(t)
	device := &Device{
		ID: "legacy-server-exit-client", Name: "Legacy Client",
		Fingerprint: "legacy-server-exit-fp", InstallationID: "legacy-server-exit-install",
		ApprovalState:         EnrollmentApproved,
		RequestedCapabilities: []string{"proxy.client"},
		ApprovedCapabilities:  []string{"proxy.client"},
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.AuthorizeServerExit(device.ID); err != nil || !allowed {
		t.Fatalf("legacy proxy client lost server exit access: allowed=%v err=%v", allowed, err)
	}
}
