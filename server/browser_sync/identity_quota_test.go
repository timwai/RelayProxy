package browsersync

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Fixture rows simulate a pre-existing installation with many approved
// browsers. Enrollment and approval of the new device use the real APIs.
func seedApprovedBrowsers(t *testing.T, store *Store, identityID string, count int) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ids := make([]string, 0, count)
	now := time.Now().UTC()
	for i := 0; i < count; i++ {
		id := "browser_" + uuid.NewString()
		ids = append(ids, id)
		if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_devices (
			id,name,signing_key,encryption_key,identity_id,state,can_send,can_receive,created_at,updated_at
		) VALUES (?,?,?,?,?,'approved',1,1,?,?)`,
			id, "quota fixture", fmt.Sprintf("quota-sign-%s-%d", identityID, i),
			fmt.Sprintf("quota-enc-%s-%d", identityID, i), identityID, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func pendingQuotaBrowser(t *testing.T, store *Store) string {
	t.Helper()
	id := "browser_" + uuid.NewString()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	if _, err := store.Register(context.Background(), BrowserDevice{
		ID: id, Name: "quota candidate", SigningKey: signing, EncryptionKey: encryption,
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApprovedBrowserIdentityDeviceQuotaAndRevocation(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	seed := seedApprovedBrowsers(t, store, "id_1", maxApprovedBrowserDevicesPerIdentity)
	pending := pendingQuotaBrowser(t, store)
	if err := store.Approve(ctx, pending, "id_1", true, false); !errors.Is(err, ErrBrowserIdentityQuota) {
		t.Fatalf("identity approved-device budget bypassed: %v", err)
	}
	if got, err := store.Device(ctx, pending); err != nil || got.State != "pending" {
		t.Fatalf("quota rejection changed pending browser: %+v %v", got, err)
	}
	if err := store.Revoke(ctx, seed[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(ctx, pending, "id_1", true, false); err != nil {
		t.Fatalf("revocation did not release an approval slot: %v", err)
	}
	if err := store.Approve(ctx, pending, "id_1", true, false); !errors.Is(err, ErrBrowserIdentityQuota) {
		// Already-approved devices must never be approved a second time.
		// Quota checks may reject before the pending-state guard, but no
		// duplicate admission or additional audit record may be created.
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("repeated approval returned unexpected result: %v", err)
		}
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_devices
		WHERE identity_id='id_1' AND state='approved'`).Scan(&count); err != nil || count != maxApprovedBrowserDevicesPerIdentity {
		t.Fatalf("identity device quota drifted: %d %v", count, err)
	}
}

func TestExtensionEnrollmentCannotBypassIdentityDeviceQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	seed := seedApprovedBrowsers(t, store, "id_1", maxApprovedBrowserDevicesPerIdentity)
	pending := pendingQuotaBrowser(t, store)
	const extensionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := store.RequestExtension(ctx, extensionID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_device_extensions
		(device_id,extension_id,created_at) VALUES(?,?,?)`, pending, extensionID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveEnrollment(ctx, pending, "id_1", false, true); !errors.Is(err, ErrBrowserIdentityQuota) {
		t.Fatalf("extension device bypassed common identity budget: %v", err)
	}
	if err := store.Revoke(ctx, seed[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveEnrollment(ctx, pending, "id_1", false, true); err != nil {
		t.Fatalf("extension-device slot was not released after revocation: %v", err)
	}
}

func TestBrowserRuleQuotaSpansMultipleDevicesWithinIdentity(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	source, target, _ := approvedDeliveryRule(t, store)
	// Five senders + five receivers, each below the 32-rule device limit.
	ids := seedApprovedBrowsers(t, store, "id_1", 8)
	sources := []string{source.ID, ids[0], ids[1], ids[2], ids[3]}
	targets := []string{target.ID, ids[4], ids[5], ids[6], ids[7]}
	first := uuid.NewString()
	now := time.Now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < maxBrowserRulesPerIdentity-1; i++ {
		ruleID := uuid.NewString()
		if i == 0 {
			ruleID = first
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_rules
			(id,source_id,target_id,policy_digest,status,source_approved,target_approved,keys_confirmed,created_at,updated_at)
			VALUES(?,?,?,'fixture','offered',1,0,0,?,?)`,
			ruleID, sources[i%len(sources)], targets[i%len(targets)], now, now); err != nil {
			t.Fatalf("existing rule fixture %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// approvedDeliveryRule already created one active pair.
	if err := store.OfferRule(ctx, source.ID, encryptedTestOffer(uuid.NewString(), target.ID)); !errors.Is(err, ErrBrowserIdentityQuota) {
		t.Fatalf("identity rule quota bypassed across devices: %v", err)
	}
	if err := store.RevokeRule(ctx, target.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := store.OfferRule(ctx, source.ID, encryptedTestOffer(uuid.NewString(), target.ID)); err != nil {
		t.Fatalf("revoked rule did not free identity capacity: %v", err)
	}
}
