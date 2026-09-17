package repository

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNewDatabaseGenerationAndEnrollmentApproval(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &User{Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "fingerprint", InstallationID: "installation", PublicKey: []byte("public-key"),
		DeviceName: "workstation", RequestedCapabilities: []string{"proxy.client"},
	}
	pending, err := db.ObserveDeviceIdentity(observation)
	if err != nil || pending.State != EnrollmentPending || pending.RequestID == "" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	approved, err := db.ApproveEnrollment(pending.RequestID, admin.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := db.ObserveDeviceIdentity(observation)
	if err != nil || decision.State != EnrollmentApproved || decision.DeviceID != approved.ID {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if err := db.RevokeDevice(approved.ID, admin.ID, "test revoke"); err != nil {
		t.Fatal(err)
	}
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM authorization_audit WHERE target_id = ?`, approved.ID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("authorization audit count=%d err=%v", auditCount, err)
	}
}

func TestRDPApprovalCreatesOwnerScopedTargetGrant(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "rdp.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &User{Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	observe := func(fingerprint, name string, capabilities []string) *EnrollmentRequest {
		t.Helper()
		decision, err := db.ObserveDeviceIdentity(DeviceIdentityObservation{
			Fingerprint: fingerprint, InstallationID: fingerprint + "-install", PublicKey: []byte(fingerprint + "-key"),
			DeviceName: name, RequestedCapabilities: capabilities,
		})
		if err != nil || decision.State != EnrollmentPending {
			t.Fatalf("observe %s: decision=%+v err=%v", name, decision, err)
		}
		return &EnrollmentRequest{ID: decision.RequestID}
	}
	controllerRequest := observe("controller-fingerprint", "Controller", []string{"rdp.controller"})
	controller, err := db.ApproveEnrollment(controllerRequest.ID, admin.ID, []string{"rdp.controller"})
	if err != nil {
		t.Fatal(err)
	}
	targetRequest := observe("target-fingerprint", "Target", []string{"rdp.host"})
	target, err := db.ApproveEnrollment(targetRequest.ID, admin.ID, []string{"rdp.host"})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := db.ListRDPTargetsForController(controller.ID)
	if err != nil || len(targets) != 1 || targets[0].DeviceID != target.ID || targets[0].Port != 3389 {
		t.Fatalf("unexpected RDP targets: %+v err=%v", targets, err)
	}
	if ok, err := db.AuthorizeRDP(controller.ID, target.ID); err != nil || !ok {
		t.Fatalf("RDP authorization failed: ok=%v err=%v", ok, err)
	}
}

func TestUpdateDeviceCapabilitiesReconcilesRDPResources(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "capabilities.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &User{Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	requested := []string{"proxy.client", "proxy.exit", "rdp.controller", "rdp.host", "rdp.public"}
	pending, err := db.ObserveDeviceIdentity(DeviceIdentityObservation{
		Fingerprint: "capability-fingerprint", InstallationID: "capability-install", PublicKey: []byte("capability-key"),
		DeviceName: "capability-device", RequestedCapabilities: requested,
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollment(pending.RequestID, admin.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := db.UpdateDeviceCapabilities(device.ID, admin.ID, requested)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ApprovedCapabilities) != len(requested) || updated.DeviceMode != "BOTH" {
		t.Fatalf("unexpected updated device: %+v", updated)
	}
	var grantCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM device_grants WHERE device_id = ?`, device.ID).Scan(&grantCount); err != nil || grantCount != len(requested) {
		t.Fatalf("grant count=%d err=%v", grantCount, err)
	}
	var serviceEnabled int
	if err := db.QueryRow(`SELECT enabled FROM rdp_services WHERE device_id = ?`, device.ID).Scan(&serviceEnabled); err != nil || serviceEnabled != 1 {
		t.Fatalf("RDP service enabled=%d err=%v", serviceEnabled, err)
	}
	if _, err := db.CreateRDPIngress(device.ID, admin.ID, 33901, nil, nil, 120, 33900, 34000); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateDeviceCapabilities(device.ID, admin.ID, []string{"proxy.client", "rdp.controller"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT enabled FROM rdp_services WHERE device_id = ?`, device.ID).Scan(&serviceEnabled); err != nil || serviceEnabled != 0 {
		t.Fatalf("RDP service remained enabled=%d err=%v", serviceEnabled, err)
	}
	var ingressStatus string
	if err := db.QueryRow(`SELECT status FROM rdp_port_allocations WHERE listen_port = 33901`).Scan(&ingressStatus); err != nil || ingressStatus != "disabled" {
		t.Fatalf("RDP ingress status=%q err=%v", ingressStatus, err)
	}
	if _, err := db.UpdateDeviceCapabilities(device.ID, admin.ID, []string{"rdp.public"}); err == nil {
		t.Fatal("public ingress capability without host was accepted")
	}
}

func TestOldDatabaseIsRejectedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := OpenDB("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE schema_meta SET generation = 'old' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	_, err = OpenDB("sqlite", path)
	if !errors.Is(err, ErrIncompatibleDatabase) {
		t.Fatalf("expected incompatible database error, got %v", err)
	}
}

func TestLegacyDatabaseWithoutSchemaMetaIsRejectedUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", ensureSQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE devices (id TEXT PRIMARY KEY, token TEXT)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDB("sqlite", path); !errors.Is(err, ErrIncompatibleDatabase) {
		t.Fatalf("expected incompatible database error, got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("legacy database bytes changed during compatibility check")
	}
	raw, err = sql.Open("sqlite", ensureSQLiteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_meta'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy database was mutated: schema_meta count=%d err=%v", count, err)
	}
}
