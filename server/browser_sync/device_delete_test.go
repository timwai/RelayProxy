package browsersync

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDeleteRevokedBrowserRemovesDependentRecordsAndAllowsFreshPending(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	sourceID := "browser_" + uuid.NewString()
	targetID := "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, sourceID, true, false)
	registerBrowserForRules(t, store, targetID, false, true)
	old, err := store.Device(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	ruleID := uuid.NewString()
	if err := store.OfferRule(ctx, sourceID, encryptedTestOffer(ruleID, targetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_network_bindings(rule_id,host,exit_id,status,updated_at) VALUES(?,?,?,'offered',CURRENT_TIMESTAMP)`, ruleID, "login.example.com", "dev_exit"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_sequences(rule_id,last_sequence) VALUES(?,1)`, ruleID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_deliveries(message_id,rule_id,source_id,target_id,expires_at) VALUES(?,?,?,?,9999999999)`, uuid.NewString(), ruleID, sourceID, targetID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_delivery_results(message_id,rule_id,source_id,status,expires_at) VALUES(?,?,?,'APPLIED',9999999999)`, uuid.NewString(), ruleID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevokedDevice(ctx, sourceID); !errors.Is(err, ErrDeleteRequiresRevoke) {
		t.Fatalf("approved device removed without revoke: %v", err)
	}
	if err := store.Revoke(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevokedDevice(ctx, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Device(ctx, sourceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted device still present: %v", err)
	}
	if _, err := store.Device(ctx, targetID); err != nil {
		t.Fatalf("unrelated receiver was deleted: %v", err)
	}
	for _, table := range []string{
		"browser_sync_rules", "browser_sync_rule_offers", "browser_sync_network_bindings",
		"browser_sync_sequences", "browser_sync_deliveries", "browser_sync_delivery_results",
	} {
		var n int
		if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s retained %d records after device deletion", table, n)
		}
	}
	fresh, err := store.Register(ctx, BrowserDevice{ID: old.ID, Name: old.Name, SigningKey: old.SigningKey, EncryptionKey: old.EncryptionKey})
	if err != nil {
		t.Fatalf("old Chrome Profile cannot re-request enrollment: %v", err)
	}
	if fresh.State != "pending" || fresh.Send || fresh.Receive || fresh.IdentityID != "" {
		t.Fatalf("re-enrollment inherited revoked privileges: %+v", fresh)
	}
	if err := store.DeleteRevokedDevice(ctx, sourceID); !errors.Is(err, ErrDeleteRequiresRevoke) {
		t.Fatalf("pending registration removed: %v", err)
	}
	if err := store.DeleteRevokedDevice(ctx, sourceID+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad device ID accepted: %v", err)
	}
	devices, err := store.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices[0].ID != sourceID || devices[0].State != "pending" {
		t.Fatalf("new pending request was not prioritized: %+v", devices)
	}
}

func TestDeleteRevokedExtensionRequiresLinkedDeviceCleanupAndFreshAdminApproval(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, nil)
	ctx := context.Background()
	device := unifiedRegistration(t)
	if out := postEnrollment(t, h, unifiedExtensionID, device); out.Code != 202 {
		t.Fatalf("initial registration %d: %s", out.Code, out.Body)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, true); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevokedExtension(ctx, unifiedExtensionID); !errors.Is(err, ErrDeleteRequiresRevoke) {
		t.Fatalf("approved extension deleted: %v", err)
	}
	if err := store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetExtensionApproved(ctx, unifiedExtensionID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevokedExtension(ctx, unifiedExtensionID); !errors.Is(err, ErrExtensionStillLinked) {
		t.Fatalf("linked device not protected: %v", err)
	}
	if out := postEnrollment(t, h, unifiedExtensionID, device); out.Code != 403 {
		t.Fatalf("revoked extension registered: %d", out.Code)
	}
	if err := store.DeleteRevokedDevice(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRevokedExtension(ctx, unifiedExtensionID); err != nil {
		t.Fatal(err)
	}
	if store.ExtensionApproved(ctx, unifiedExtensionID) {
		t.Fatal("deleted extension still approved")
	}
	if out := postEnrollment(t, h, unifiedExtensionID, device); out.Code != 202 {
		t.Fatalf("fresh request blocked: %d %s", out.Code, out.Body)
	}
	saved, err := store.Device(ctx, device.ID)
	if err != nil || saved.State != "pending" || saved.ExtensionState != "approved" || saved.Send || saved.Receive {
		t.Fatalf("old privileges survived fresh application: %+v %v", saved, err)
	}
	if !store.ExtensionApproved(ctx, unifiedExtensionID) {
		t.Fatal("new extension Origin was not registered as metadata")
	}
	if err := store.DeleteRevokedExtension(ctx, unifiedExtensionID); !errors.Is(err, ErrDeleteRequiresRevoke) {
		t.Fatalf("pending extension deleted without revocation: %v", err)
	}
}
