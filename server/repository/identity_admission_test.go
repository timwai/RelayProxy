package repository

import (
	"errors"
	"testing"
	"time"
)

func TestIdentityEnrollmentRechecksChallengeSnapshot(t *testing.T) {
	for _, mutation := range []string{"revoke", "expire", "disable"} {
		t.Run(mutation, func(t *testing.T) {
			db := openIdentityTestDB(t)
			identity, err := db.CreateIdentity("Owner", "admin")
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
			}
			if err != nil {
				t.Fatal(err)
			}
			observation := DeviceIdentityObservation{
				Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
				RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
			}
			if _, err := db.ObserveIdentityDevice(*access, observation); !errors.Is(err, ErrInvalidIdentityAccessKey) {
				t.Fatalf("stale challenge admitted after %s: %v", mutation, err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("enrollment left devices: %d %v", n, err)
			}
		})
	}
}

func TestIdentityEnrollmentRefreshesMetadataWithoutFilteringDeviceCapabilities(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Before", "admin")
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
	if _, err := db.Exec(`UPDATE identities SET capabilities = ? WHERE id = ?`, `["proxy.client"]`, identity.ID); err != nil {
		t.Fatal(err)
	}
	after := "After"
	if _, err := db.UpdateIdentity(identity.ID, "admin", IdentityUpdate{Name: &after}); err != nil {
		t.Fatal(err)
	}
	decision, err := db.ObserveIdentityDevice(*access, DeviceIdentityObservation{
		Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
		RequestedCapabilities: []string{"proxy.client", "proxy.exit"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.IdentityName != after || decision.PolicyRevision != 2 || len(decision.ApprovedCapabilities) != 2 {
		t.Fatalf("identity metadata or device capabilities were not refreshed independently: %+v", decision)
	}
}

func TestDeviceCapabilitiesControlAdmissionAndFeatures(t *testing.T) {
	db := openIdentityTestDB(t)
	identity, err := db.CreateIdentity("Owner", "admin")
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
	observation := DeviceIdentityObservation{
		Fingerprint: "fp", InstallationID: "install", PublicKey: []byte("public"),
		RequestedCapabilities: []string{"proxy.client", "rdp.controller"},
	}
	client, err := db.ObserveIdentityDevice(*access, observation)
	if err != nil {
		t.Fatal(err)
	}
	seedIdentityGrantDevice(t, db, "target", "Target", identity.ID, []string{"proxy.exit", "rdp.host"})

	if _, err := db.UpdateDeviceCapabilities(client.DeviceID, "admin", []string{"proxy.client"}); err != nil {
		t.Fatal(err)
	}
	if !db.IsIdentityDeviceAuthorized("fp", client.DeviceID, identity.ID, key.ID) {
		t.Fatal("device capability change unexpectedly invalidated identity authentication")
	}
	managed, allowed, err := db.authorizeIdentityDeviceFeature(client.DeviceID, "target", GrantFeatureProxyUse)
	if err != nil || !managed || !allowed {
		t.Fatalf("approved proxy capability was not honored: %v %v %v", managed, allowed, err)
	}
	managed, allowed, err = db.authorizeIdentityDeviceFeature(client.DeviceID, "target", GrantFeatureRDPConnect)
	if err != nil || !managed || allowed {
		t.Fatalf("removed RDP device capability remained effective: %v %v %v", managed, allowed, err)
	}

	reconnected, err := db.ObserveIdentityDevice(*access, observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconnected.ApprovedCapabilities) != 1 || reconnected.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("reconnect self-expanded device capabilities: %+v", reconnected.ApprovedCapabilities)
	}
}
