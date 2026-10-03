package repository

import (
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
}

func TestDeviceIdentityGrantLifecycleAndAuthorization(t *testing.T) {
	db := openIdentityTestDB(t)

	owner, err := db.CreateIdentity("Owner", "admin", []string{"proxy.client", "proxy.exit", "rdp.controller", "rdp.host"})
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Grantee", "admin", []string{"proxy.client", "rdp.controller"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateIdentity("Other", "admin", []string{"proxy.client"})
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

	grant, err := db.CreateDeviceIdentityGrant("target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grant.Revision != 1 || len(grant.Features) != 1 || grant.Features[0] != GrantFeatureProxyUse {
		t.Fatalf("unexpected grant: %+v", grant)
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
}

func TestDeviceIdentityGrantExpiryAndTargetCapabilityValidation(t *testing.T) {
	db := openIdentityTestDB(t)
	owner, err := db.CreateIdentity("Target Owner", "admin", []string{"proxy.exit"})
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Client Identity", "admin", []string{"proxy.client"})
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
