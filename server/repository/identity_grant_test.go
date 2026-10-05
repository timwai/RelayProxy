package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func seedIdentityGrantDevice(t *testing.T, db *DB, id, name, identityID string, capabilities []string) {
	t.Helper()
	device := &Device{
		ID: id, Name: name, Fingerprint: "fp-" + id, InstallationID: "install-" + id,
		ApprovalState:         EnrollmentApproved,
		RequestedCapabilities: append([]string(nil), capabilities...),
		ApprovedCapabilities:  append([]string(nil), capabilities...),
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(id, identityID, "admin"); err != nil {
		t.Fatal(err)
	}
	if containsCapabilityValue(capabilities, "rdp.host") {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := syncDeviceRDPService(tx, id, name, capabilities, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeviceIdentityGrantLifecycleAndAuthorization(t *testing.T) {
	db := openIdentityTestDB(t)

	owner, err := db.CreateIdentity("Owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Grantee", "admin")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateIdentity("Other", "admin")
	if err != nil {
		t.Fatal(err)
	}

	seedIdentityGrantDevice(t, db, "target", "Target", owner.ID, []string{"proxy.exit", "rdp.host"})
	seedIdentityGrantDevice(t, db, "same-client", "Same Client", owner.ID, []string{"proxy.client", "rdp.controller"})
	seedIdentityGrantDevice(t, db, "grantee-client", "Grantee Client", grantee.ID, []string{"proxy.client", "rdp.controller"})
	seedIdentityGrantDevice(t, db, "other-client", "Other Client", other.ID, []string{"proxy.client"})

	managed, allowed, err := db.authorizeIdentityDeviceFeature("same-client", "target", GrantFeatureProxyUse)
	if err != nil || !managed || !allowed {
		t.Fatalf("same-identity proxy access = managed:%v allowed:%v err:%v", managed, allowed, err)
	}

	if allowed, err := db.AuthorizeClientExit("same-client", "target"); err != nil || !allowed {
		t.Fatalf("same-identity exported proxy authorization = allowed:%v err:%v", allowed, err)
	}
	if allowed, err := db.AuthorizeRDP("same-client", "target"); err != nil || !allowed {
		t.Fatalf("same-identity exported RDP authorization = allowed:%v err:%v", allowed, err)
	}
	sameTargets, err := db.ListRDPTargetsForController("same-client")
	if err != nil || len(sameTargets) != 1 || sameTargets[0].DeviceID != "target" {
		t.Fatalf("same-identity RDP target list = %+v err:%v", sameTargets, err)
	}
	if allowed, err := db.AuthorizeClientExit("grantee-client", "target"); err != nil || allowed {
		t.Fatalf("ungranted cross-identity proxy authorization = allowed:%v err:%v", allowed, err)
	}

	grant, err := db.CreateDeviceIdentityGrant("target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Revision != 1 || len(grant.Features) != 1 || grant.Features[0] != GrantFeatureProxyUse {
		t.Fatalf("unexpected grant: %+v", grant)
	}

	if allowed, err := db.AuthorizeClientExit("grantee-client", "target"); err != nil || !allowed {
		t.Fatalf("exported cross-identity proxy authorization = allowed:%v err:%v", allowed, err)
	}
	if allowed, err := db.AuthorizeRDP("grantee-client", "target"); err != nil || allowed {
		t.Fatalf("proxy-only grant unexpectedly authorized RDP = allowed:%v err:%v", allowed, err)
	}
	granteeTargets, err := db.ListRDPTargetsForController("grantee-client")
	if err != nil || len(granteeTargets) != 0 {
		t.Fatalf("proxy-only grant leaked RDP target = %+v err:%v", granteeTargets, err)
	}
	if _, err := db.CreateDeviceIdentityGrant("target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, nil); !errors.Is(err, ErrDeviceIdentityGrantExists) {
		t.Fatalf("duplicate grant accepted: %v", err)
	}
	if _, err := db.CreateDeviceIdentityGrant("target", owner.ID, "admin", []string{GrantFeatureProxyUse}, nil); err == nil {
		t.Fatal("same-identity explicit grant was accepted")
	}

	managed, allowed, err = db.authorizeIdentityDeviceFeature("grantee-client", "target", GrantFeatureProxyUse)
	if err != nil || !managed || !allowed {
		t.Fatalf("cross-identity proxy grant not applied = managed:%v allowed:%v err:%v", managed, allowed, err)
	}
	_, allowed, err = db.authorizeIdentityDeviceFeature("grantee-client", "target", GrantFeatureRDPConnect)
	if err != nil || allowed {
		t.Fatalf("RDP was implicitly granted = allowed:%v err:%v", allowed, err)
	}
	_, allowed, err = db.authorizeIdentityDeviceFeature("other-client", "target", GrantFeatureProxyUse)
	if err != nil || allowed {
		t.Fatalf("ungranted identity gained proxy access = allowed:%v err:%v", allowed, err)
	}

	features := []string{GrantFeatureProxyUse, GrantFeatureRDPConnect}
	updated, err := db.UpdateDeviceIdentityGrant(grant.ID, "admin", DeviceIdentityGrantUpdate{
		Features: &features, Revision: grant.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || len(updated.Features) != 2 {
		t.Fatalf("unexpected updated grant: %+v", updated)
	}

	if allowed, err := db.AuthorizeRDP("grantee-client", "target"); err != nil || !allowed {
		t.Fatalf("exported cross-identity RDP authorization = allowed:%v err:%v", allowed, err)
	}
	granteeTargets, err = db.ListRDPTargetsForController("grantee-client")
	if err != nil || len(granteeTargets) != 1 || granteeTargets[0].DeviceID != "target" {
		t.Fatalf("cross-identity RDP target list = %+v err:%v", granteeTargets, err)
	}
	_, allowed, err = db.authorizeIdentityDeviceFeature("grantee-client", "target", GrantFeatureRDPConnect)
	if err != nil || !allowed {
		t.Fatalf("RDP grant not applied = allowed:%v err:%v", allowed, err)
	}

	if _, err := db.UpdateDeviceIdentityGrant(grant.ID, "admin", DeviceIdentityGrantUpdate{
		Features: &features, Revision: 1,
	}); !errors.Is(err, ErrDeviceIdentityGrantRevision) {
		t.Fatalf("stale update revision accepted: %v", err)
	}
	if _, err := db.DeleteDeviceIdentityGrant(grant.ID, "admin", 1); !errors.Is(err, ErrDeviceIdentityGrantRevision) {
		t.Fatalf("stale delete revision accepted: %v", err)
	}
	deleted, err := db.DeleteDeviceIdentityGrant(grant.ID, "admin", updated.Revision)
	if err != nil || !deleted {
		t.Fatalf("delete failed: deleted=%v err=%v", deleted, err)
	}
	deleted, err = db.DeleteDeviceIdentityGrant(grant.ID, "admin", updated.Revision)
	if err != nil || deleted {
		t.Fatalf("idempotent delete failed: deleted=%v err=%v", deleted, err)
	}
	_, allowed, err = db.authorizeIdentityDeviceFeature("grantee-client", "target", GrantFeatureProxyUse)
	if err != nil || allowed {
		t.Fatalf("deleted grant still authorizes = allowed:%v err:%v", allowed, err)
	}

	if allowed, err := db.AuthorizeClientExit("grantee-client", "target"); err != nil || allowed {
		t.Fatalf("deleted grant still authorizes exported proxy path = allowed:%v err:%v", allowed, err)
	}
	if allowed, err := db.AuthorizeRDP("grantee-client", "target"); err != nil || allowed {
		t.Fatalf("deleted grant still authorizes exported RDP path = allowed:%v err:%v", allowed, err)
	}
	granteeTargets, err = db.ListRDPTargetsForController("grantee-client")
	if err != nil || len(granteeTargets) != 0 {
		t.Fatalf("deleted grant still visible in RDP target list = %+v err:%v", granteeTargets, err)
	}
}

func TestDeviceIdentityGrantExpiryAndTargetCapabilityValidation(t *testing.T) {
	db := openIdentityTestDB(t)
	owner, err := db.CreateIdentity("Target Owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Client Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "limited-target", "Limited", owner.ID, []string{"proxy.exit"})
	seedIdentityGrantDevice(t, db, "limited-client", "Client", grantee.ID, []string{"proxy.client"})

	if _, err := db.CreateDeviceIdentityGrant("limited-target", grantee.ID, "admin", []string{GrantFeatureRDPConnect}, nil); err == nil {
		t.Fatal("RDP grant accepted for a non-RDP target")
	}
	expires := time.Now().UTC().Add(time.Hour)
	grant, err := db.CreateDeviceIdentityGrant("limited-target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, &expires)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE device_identity_grants SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), grant.ID); err != nil {
		t.Fatal(err)
	}
	_, allowed, err := db.authorizeIdentityDeviceFeature("limited-client", "limited-target", GrantFeatureProxyUse)
	if err != nil || allowed {
		t.Fatalf("expired grant authorized access = allowed:%v err:%v", allowed, err)
	}

	disabled := IdentityStatusDisabled
	if _, err := db.UpdateIdentity(grantee.ID, "admin", IdentityUpdate{Status: &disabled, PolicyRevision: grantee.PolicyRevision}); err != nil {
		t.Fatal(err)
	}
	_, allowed, err = db.authorizeIdentityDeviceFeature("limited-client", "limited-target", GrantFeatureProxyUse)
	if err != nil || allowed {
		t.Fatalf("disabled grantee identity authorized access = allowed:%v err:%v", allowed, err)
	}
}

func TestDeviceIdentityMoveRevokesTargetShares(t *testing.T) {
	db := openIdentityTestDB(t)
	source, err := db.CreateIdentity("Source Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := db.CreateIdentity("Destination Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Shared Client Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}

	seedIdentityGrantDevice(t, db, "moving-target", "Moving Target", source.ID, []string{"proxy.exit"})
	seedIdentityGrantDevice(t, db, "moving-client", "Moving Client", grantee.ID, []string{"proxy.client"})
	grant, err := db.CreateDeviceIdentityGrant("moving-target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := db.AuthorizeClientExit("moving-client", "moving-target"); err != nil || !allowed {
		t.Fatalf("grant did not authorize before identity move: allowed=%v err=%v", allowed, err)
	}

	summary, err := db.SetDeviceIdentity("moving-target", destination.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if summary.IdentityID != destination.ID {
		t.Fatalf("target identity move failed: %+v", summary)
	}
	if _, err := db.GetDeviceIdentityGrant(grant.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("target share survived identity move: %v", err)
	}
	if allowed, err := db.AuthorizeClientExit("moving-client", "moving-target"); err != nil || allowed {
		t.Fatalf("old grantee retained access after target identity move: allowed=%v err=%v", allowed, err)
	}
}


func TestPublicDirectAuthorizationSnapshotBindsPolicyAndGrantRevision(t *testing.T) {
	db := openIdentityTestDB(t)
	exitIdentity, err := db.CreateIdentity("Direct Exit Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	clientIdentity, err := db.CreateIdentity("Direct Client Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "direct-exit", "Direct Exit", exitIdentity.ID, []string{"proxy.exit"})
	seedIdentityGrantDevice(t, db, "direct-same-client", "Direct Same Client", exitIdentity.ID, []string{"proxy.client"})
	seedIdentityGrantDevice(t, db, "direct-cross-client", "Direct Cross Client", clientIdentity.ID, []string{"proxy.client"})

	same, err := db.PublicDirectAuthorization("direct-same-client", "direct-exit")
	if err != nil {
		t.Fatal(err)
	}
	if !same.Allowed || same.PolicyRevision != exitIdentity.PolicyRevision ||
		same.AuthorizationRevision != exitIdentity.PolicyRevision {
		t.Fatalf("same-identity snapshot=%+v", same)
	}

	before, err := db.PublicDirectAuthorization("direct-cross-client", "direct-exit")
	if err != nil {
		t.Fatal(err)
	}
	if before.Allowed {
		t.Fatalf("ungranted cross-identity snapshot=%+v", before)
	}

	grant, err := db.CreateDeviceIdentityGrant(
		"direct-exit", clientIdentity.ID, "admin", []string{GrantFeatureProxyUse}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	cross, err := db.PublicDirectAuthorization("direct-cross-client", "direct-exit")
	if err != nil {
		t.Fatal(err)
	}
	if !cross.Allowed || cross.PolicyRevision != clientIdentity.PolicyRevision ||
		cross.AuthorizationRevision != grant.Revision {
		t.Fatalf("cross-identity snapshot=%+v grant=%+v", cross, grant)
	}

	newName := "Direct Client Identity Updated"
	updatedIdentity, err := db.UpdateIdentity(clientIdentity.ID, "admin", IdentityUpdate{
		Name: &newName, PolicyRevision: clientIdentity.PolicyRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	cross, err = db.PublicDirectAuthorization("direct-cross-client", "direct-exit")
	if err != nil {
		t.Fatal(err)
	}
	if !cross.Allowed || cross.PolicyRevision != updatedIdentity.PolicyRevision ||
		cross.AuthorizationRevision != grant.Revision {
		t.Fatalf("policy revision not reflected in snapshot=%+v", cross)
	}

	features := []string{GrantFeatureProxyUse, GrantFeatureRDPConnect}
	updatedGrant, err := db.UpdateDeviceIdentityGrant(grant.ID, "admin", DeviceIdentityGrantUpdate{
		Features: &features, Revision: grant.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	cross, err = db.PublicDirectAuthorization("direct-cross-client", "direct-exit")
	if err != nil {
		t.Fatal(err)
	}
	if !cross.Allowed || cross.AuthorizationRevision != updatedGrant.Revision {
		t.Fatalf("grant revision not reflected in snapshot=%+v", cross)
	}
}
