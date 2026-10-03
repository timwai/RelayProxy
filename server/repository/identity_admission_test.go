package repository

import (
	"errors"
	"testing"
	"time"
)

func TestIdentityEnrollmentRechecksChallengeSnapshot(t *testing.T) {
	for _, mutation := range []string{"revoke", "expire", "disable", "policy"} {
		t.Run(mutation, func(t *testing.T) {
			db := openIdentityTestDB(t)
			identity, err := db.CreateIdentity("Owner", "admin", []string{"proxy.client", "proxy.exit"})
			if err != nil {
				t.Fatal(err)
			}
			key, err := db.IssueIdentityAccessKey(identity.ID, "admin", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			access, err := db.ResolveIdentityAccessKey(key.AccessKey)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "revoke":
				err = db.RevokeIdentityAccessKey(identity.ID, key.ID, "admin")
			case "expire":
				_, err = db.Exec(`UPDATE identity_access_keys SET expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), key.ID)
			case "disable":
				status := IdentityStatusDisabled
				_, err = db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Status: &status})
			case "policy":
				caps := []string{"proxy.client"}
				_, err = db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Capabilities: &caps})
			}
			if err != nil {
				t.Fatal(err)
			}
			observation := DeviceIdentityObservation{Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"), RequestedCapabilities: []string{"proxy.client", "proxy.exit"}}
			decision, err := db.ObserveIdentityDevice(*access, observation)
			if mutation == "policy" {
				if err != nil {
					t.Fatal(err)
				}
				if decision.PolicyRevision != 2 || len(decision.ApprovedCapabilities) != 1 || decision.ApprovedCapabilities[0] != "proxy.client" {
					t.Fatalf("stale policy enrolled: %+v", decision)
				}
				return
			}
			if !errors.Is(err, ErrInvalidIdentityAccessKey) {
				t.Fatalf("revoked challenge admitted: %v", err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("enrollment left devices: %d %v", n, err)
			}
		})
	}
}

func TestIdentityPolicyTighteningRevokesAdmissionAndBothFeatures(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Owner", "admin", []string{"proxy.client", "proxy.exit", "rdp.controller", "rdp.host"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := db.IssueIdentityAccessKey(identity.ID, "admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	access, err := db.ResolveIdentityAccessKey(key.AccessKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := db.ObserveIdentityDevice(*access, DeviceIdentityObservation{Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"), RequestedCapabilities: []string{"proxy.client", "rdp.controller"}})
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "target", "Target", identity.ID, []string{"proxy.exit", "rdp.host"})
	if !db.IsIdentityDeviceAuthorized("fp", client.DeviceID, identity.ID, key.ID) {
		t.Fatal("fresh device rejected")
	}
	caps := []string{"proxy.exit", "rdp.host"}
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Capabilities: &caps}); err != nil {
		t.Fatal(err)
	}
	if db.IsIdentityDeviceAuthorized("fp", client.DeviceID, identity.ID, key.ID) {
		t.Fatal("stale session capabilities accepted")
	}
	for _, feature := range []string{GrantFeatureProxyUse, GrantFeatureRDPConnect} {
		managed, allowed, err := db.authorizeIdentityDeviceFeature(client.DeviceID, "target", feature)
		if err != nil || !managed || allowed {
			t.Fatalf("feature %s survived policy tightening: %v %v %v", feature, managed, allowed, err)
		}
	}
}
