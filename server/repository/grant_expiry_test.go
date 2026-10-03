package repository

import (
	"database/sql"
	"testing"
	"time"
)

func TestExpireIdentityGrantsRemovesExpiredRowsAndDeduplicatesIdentities(t *testing.T) {
	db := openIdentityTestDB(t)

	targetIdentity, err := db.CreateIdentity("Expiry Target", "admin", []string{"proxy.exit"})
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := db.CreateIdentity("Expiry Grantee", "admin", []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateIdentity("Expiry Other", "admin", []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "expiry-target", "Expiry Target Device", targetIdentity.ID, []string{"proxy.exit"})

	future := time.Now().UTC().Add(time.Hour)
	deviceGrant, err := db.CreateDeviceIdentityGrant(
		"expiry-target", grantee.ID, "admin", []string{GrantFeatureProxyUse}, &future,
	)
	if err != nil {
		t.Fatal(err)
	}
	systemGrant, err := db.CreateSystemIdentityGrant(
		SystemResourceServerExit, grantee.ID, "admin", []string{GrantFeatureProxyUse}, &future,
	)
	if err != nil {
		t.Fatal(err)
	}
	activeGrant, err := db.CreateSystemIdentityGrant(
		SystemResourceServerExit, other.ID, "admin", []string{GrantFeatureProxyUse}, &future,
	)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	if _, err := db.Exec(`UPDATE device_identity_grants SET expires_at = ? WHERE id = ?`, past, deviceGrant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE system_identity_grants SET expires_at = ? WHERE id = ?`, past, systemGrant.ID); err != nil {
		t.Fatal(err)
	}

	affected, err := db.ExpireIdentityGrants(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 1 || affected[0] != grantee.ID {
		t.Fatalf("affected identities=%v want [%s]", affected, grantee.ID)
	}
	if _, err := db.GetDeviceIdentityGrant(deviceGrant.ID); !isNoRows(err) {
		t.Fatalf("expired device grant still exists: %v", err)
	}
	if _, err := db.GetSystemIdentityGrant(systemGrant.ID); !isNoRows(err) {
		t.Fatalf("expired system grant still exists: %v", err)
	}
	if got, err := db.GetSystemIdentityGrant(activeGrant.ID); err != nil || got.ID != activeGrant.ID {
		t.Fatalf("active system grant was removed: got=%+v err=%v", got, err)
	}

	affected, err = db.ExpireIdentityGrants(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 0 {
		t.Fatalf("second expiry sweep returned identities again: %v", affected)
	}
}

func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}
