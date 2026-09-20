package repository

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestDeleteDeviceAllowsFreshEnrollment(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "delete-device.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	admin := &User{Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "delete-fingerprint", InstallationID: "delete-installation", PublicKey: []byte("delete-key"),
		DeviceName: "delete-me", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"proxy.client", "rdp.host"},
	}
	pending, err := db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollment(pending.RequestID, admin.ID, []string{"proxy.client", "rdp.host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteDevice(device.ID, admin.ID); err != nil {
		t.Fatal(err)
	}

	var deviceCount, identityCount, enrollmentCount, grantCount, rdpServiceCount int
	for query, dst := range map[string]*int{
		`SELECT COUNT(*) FROM devices WHERE id = ?`:                    &deviceCount,
		`SELECT COUNT(*) FROM device_identities WHERE fingerprint = ?`: &identityCount,
		`SELECT COUNT(*) FROM device_enrollment_requests WHERE fingerprint = ?`: &enrollmentCount,
		`SELECT COUNT(*) FROM device_grants WHERE device_id = ?`:        &grantCount,
		`SELECT COUNT(*) FROM rdp_services WHERE device_id = ?`:        &rdpServiceCount,
	} {
		arg := any(device.ID)
		if strings.Contains(query, "fingerprint") {
			arg = observation.Fingerprint
		}
		if err := db.QueryRow(query, arg).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if deviceCount != 0 || identityCount != 0 || enrollmentCount != 0 || grantCount != 0 || rdpServiceCount != 0 {
		t.Fatalf("device delete left rows: device=%d identity=%d enrollment=%d grants=%d rdp=%d",
			deviceCount, identityCount, enrollmentCount, grantCount, rdpServiceCount)
	}

	decision, err := db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != EnrollmentPending || decision.RequestID == "" || decision.DeviceID != "" {
		t.Fatalf("deleted installation did not return as fresh pending enrollment: %+v", decision)
	}
	if decision.RequestID == pending.RequestID {
		t.Fatal("deleted installation reused its old enrollment request")
	}
}

func TestApprovedIdentityRefreshesRequestedCapabilitiesAndScopesSessionGrants(t *testing.T) {
	db, err := OpenDB("sqlite", filepath.Join(t.TempDir(), "refresh-capabilities.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &User{Username: "admin", PasswordHash: "hash", Role: "admin", Status: "active"}
	if err := db.CreateUser(admin); err != nil {
		t.Fatal(err)
	}
	observation := DeviceIdentityObservation{
		Fingerprint: "refresh-fingerprint", InstallationID: "refresh-install", PublicKey: []byte("refresh-key"),
		DeviceName: "before-upgrade", Platform: "windows", Arch: "amd64", ClientVersion: "1.0.0",
		RequestedCapabilities: []string{"proxy.client"},
	}
	pending, err := db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := db.ApproveEnrollment(pending.RequestID, admin.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}

	observation.DeviceName = "after-upgrade"
	observation.ClientVersion = "2.0.0"
	observation.RequestedCapabilities = []string{"proxy.client", "proxy.exit"}
	decision, err := db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.ApprovedCapabilities) != 1 || decision.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("unapproved capability leaked into session grants: %+v", decision.ApprovedCapabilities)
	}
	var requestedRaw, approvedRaw, name, version string
	if err := db.QueryRow(`SELECT requested_capabilities, approved_capabilities, name, client_version FROM devices WHERE id = ?`, device.ID).
		Scan(&requestedRaw, &approvedRaw, &name, &version); err != nil {
		t.Fatal(err)
	}
	requested, err := decodeCapabilities(requestedRaw)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := decodeCapabilities(approvedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 || requested[0] != "proxy.client" || requested[1] != "proxy.exit" {
		t.Fatalf("requested capabilities were not refreshed: %+v", requested)
	}
	if len(approved) != 1 || approved[0] != "proxy.client" || name != "after-upgrade" || version != "2.0.0" {
		t.Fatalf("unexpected persisted device state: approved=%+v name=%q version=%q", approved, name, version)
	}
	if _, err := db.UpdateDeviceCapabilities(device.ID, admin.ID, []string{"proxy.client", "proxy.exit"}); err != nil {
		t.Fatalf("administrator could not approve newly declared exit capability: %v", err)
	}

	observation.RequestedCapabilities = []string{"proxy.client"}
	decision, err = db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.ApprovedCapabilities) != 1 || decision.ApprovedCapabilities[0] != "proxy.client" {
		t.Fatalf("locally disabled exit remained in effective session grants: %+v", decision.ApprovedCapabilities)
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
