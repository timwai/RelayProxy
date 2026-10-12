package browsersync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests intentionally run independent admission attempts concurrently.
// The sqlite fixture serializes writes on one connection, matching the
// single-writer production guarantee. CI also runs the package with -race.
func TestBrowserSyncConcurrentOriginRegistrationQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	if _, err := store.RequestExtension(ctx, quotaTestExtension); err != nil {
		t.Fatal(err)
	}
	const free = 5
	seedPendingInventory(t, store, maxPendingBrowserEnrollmentsPerExtension-free, quotaTestExtension)
	const contenders = 24
	devices := make([]BrowserDevice, contenders)
	for i := range devices {
		devices[i] = quotaBrowserIdentity(t)
	}
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = store.EnrollExtensionDevice(ctx, quotaTestExtension, devices[i])
		}(i)
	}
	wg.Wait()
	won, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrBrowserEnrollmentQuota):
			limited++
		default:
			t.Fatalf("concurrent enrollment failed unexpectedly: %v", err)
		}
	}
	if won != free || limited != contenders-free {
		t.Fatalf("Origin quota admitted %d and limited %d, want %d admitted", won, limited, free)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_device_extensions x
		JOIN browser_sync_devices d ON d.id=x.device_id
		WHERE d.state='pending' AND x.extension_id=?`, quotaTestExtension).Scan(&count); err != nil ||
		count != maxPendingBrowserEnrollmentsPerExtension {
		t.Fatalf("committed Origin count=%d: %v", count, err)
	}
	for i, device := range devices {
		actual, err := store.Device(ctx, device.ID)
		if errs[i] == nil {
			if err != nil || actual.ExtensionID != quotaTestExtension || actual.State != "pending" {
				t.Fatalf("accepted enrollment lost its atomic Origin binding: %+v %v", actual, err)
			}
		} else if !errors.Is(err, ErrNotFound) {
			t.Fatalf("quota-rejected enrollment left a device row: %v", err)
		}
	}
}

func TestBrowserSyncConcurrentGlobalRegistrationQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	if _, err := store.RequestExtension(ctx, quotaTestExtension); err != nil {
		t.Fatal(err)
	}
	const free = 4
	seedPendingInventory(t, store, maxPendingBrowserEnrollmentsGlobal-free, "")
	const contenders = 20
	devices := make([]BrowserDevice, contenders)
	for i := range devices {
		devices[i] = quotaBrowserIdentity(t)
	}
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := range devices {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = store.EnrollExtensionDevice(ctx, quotaTestExtension, devices[i])
		}(i)
	}
	wg.Wait()
	won, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrBrowserEnrollmentQuota):
			limited++
		default:
			t.Fatalf("global enrollment quota returned unexpected error: %v", err)
		}
	}
	if won != free || limited != contenders-free {
		t.Fatalf("global pending quota admitted %d, rejected %d", won, limited)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_devices
		WHERE state='pending'`).Scan(&count); err != nil || count != maxPendingBrowserEnrollmentsGlobal {
		t.Fatalf("global pending count=%d: %v", count, err)
	}
}

func TestBrowserSyncConcurrentDeviceApprovalQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	const free = 3
	seedApprovedBrowsers(t, store, "id_1", maxApprovedBrowserDevicesPerIdentity-free)
	const contenders = 18
	ids := make([]string, contenders)
	for i := range ids {
		ids[i] = pendingQuotaBrowser(t, store)
	}
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.Approve(ctx, ids[i], "id_1", true, false)
		}(i)
	}
	wg.Wait()
	won, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrBrowserIdentityQuota):
			limited++
		default:
			t.Fatalf("concurrent approval failed unexpectedly: %v", err)
		}
	}
	if won != free || limited != contenders-free {
		t.Fatalf("approved %d devices, rejected %d; want %d new approvals", won, limited, free)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_devices
		WHERE state='approved' AND identity_id='id_1'`).Scan(&count); err != nil ||
		count != maxApprovedBrowserDevicesPerIdentity {
		t.Fatalf("approved devices=%d: %v", count, err)
	}
	for i, id := range ids {
		got, err := store.Device(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (errs[i] == nil) != (got.State == "approved") {
			t.Fatalf("approval result and committed state diverged for %s: %+v %v", id, got, errs[i])
		}
	}
}

func TestBrowserSyncConcurrentPairingRuleQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	source, target, _ := approvedDeliveryRule(t, store)
	more := seedApprovedBrowsers(t, store, "id_1", 8)
	sources := []string{source.ID, more[0], more[1], more[2], more[3]}
	targets := []string{target.ID, more[4], more[5], more[6], more[7]}
	const free = 7
	const contenders = 20
	now := time.Now().UTC()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxBrowserRulesPerIdentity-1-free; i++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_rules
			(id,source_id,target_id,policy_digest,status,source_approved,target_approved,keys_confirmed,created_at,updated_at)
			VALUES(?,?,?,'fixture','offered',1,0,0,?,?)`,
			uuid.NewString(), sources[i%len(sources)], targets[i%len(targets)], now, now); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.OfferRule(ctx, sources[i%len(sources)],
				encryptedTestOffer(uuid.NewString(), targets[i%len(targets)]))
		}(i)
	}
	wg.Wait()
	won, limited := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrBrowserIdentityQuota):
			limited++
		default:
			t.Fatalf("pairing admission failed unexpectedly: %v", err)
		}
	}
	if won != free || limited != contenders-free {
		t.Fatalf("pairing quota admitted %d, rejected %d; want %d", won, limited, free)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_rules r
		JOIN browser_sync_devices src ON src.id=r.source_id
		WHERE src.identity_id='id_1' AND r.status!='revoked'`).Scan(&count); err != nil ||
		count != maxBrowserRulesPerIdentity {
		t.Fatalf("identity pairing count=%d: %v", count, err)
	}
	for _, id := range sources {
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_rules
			WHERE status!='revoked' AND (source_id=? OR target_id=?)`, id, id).Scan(&count); err != nil ||
			count > maxBrowserRulesPerDevice {
			t.Fatalf("source %s exceeded per-device rule quota: count=%d err=%v", id, count, err)
		}
	}
}

func TestBrowserSyncConcurrentDeliveryAndTerminalRecovery(t *testing.T) {
	store := testBrowserStore(t)
	ctx, now := context.Background(), time.Now().UTC()
	source, target, ruleID := approvedDeliveryRule(t, store)
	const contenders = 36
	ids := make([]string, contenders)
	errs := make([]error, contenders)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = store.RecordDelivery(ctx, ids[i], ruleID, source.ID, target.ID, now)
		}(i)
	}
	wg.Wait()
	admitted := make([]string, 0, maxPendingDeliveriesPerRule)
	for i, err := range errs {
		switch {
		case err == nil:
			admitted = append(admitted, ids[i])
		case errors.Is(err, ErrRuleDenied):
		default:
			t.Fatalf("unexpected delivery admission result for %d: %v", i, err)
		}
	}
	if len(admitted) != maxPendingDeliveriesPerRule {
		t.Fatalf("concurrent deliveries admitted %d, want %d", len(admitted), maxPendingDeliveriesPerRule)
	}
	results := make([]error, len(admitted))
	for i, id := range admitted {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			_, results[i] = store.ClaimDeliveryReceipt(ctx, target.ID, ruleID, id, "APPLIED", now)
		}(i, id)
	}
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("terminal ACK %d lost during concurrent claims: %v", i, err)
		}
		state, err := store.DeliveryStatus(ctx, source.ID, ruleID, admitted[i], now)
		if err != nil || state != "APPLIED" {
			t.Fatalf("terminal ACK not recoverable: status=%q err=%v", state, err)
		}
		if _, err := store.ClaimDeliveryReceipt(ctx, target.ID, ruleID, admitted[i], "APPLIED", now); !errors.Is(err, ErrRuleDenied) {
			t.Fatalf("duplicate terminal ACK accepted: %v", err)
		}
	}
	if n, err := store.ActiveDeliveryCount(ctx, ruleID); err != nil || n != 0 {
		t.Fatalf("ACK claims did not free every pending slot: remaining=%d err=%v", n, err)
	}
	for i := 0; i < maxPendingDeliveriesPerRule; i++ {
		if err := store.RecordDelivery(ctx, uuid.NewString(), ruleID,
			source.ID, target.ID, now); err != nil {
			t.Fatalf("old terminal ACKs prevented reusing pending slots after ACK %d: %v", i, err)
		}
	}
}

// The Origin-revocation test checks a formerly unguarded gap between
// authorization and receipt mutation, including the intermediate RECEIVED ACK.
func TestBrowserSyncRevokedOriginBlocksBothReceiptStages(t *testing.T) {
	store := testBrowserStore(t)
	ctx, now := context.Background(), time.Now().UTC()
	source, target, ruleID := approvedDeliveryRule(t, store)
	if _, err := store.RequestExtension(ctx, quotaTestExtension); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO browser_sync_device_extensions
		(device_id,extension_id,created_at) VALUES(?,?,?)`, target.ID, quotaTestExtension, now); err != nil {
		t.Fatal(err)
	}
	ids := []string{uuid.NewString(), uuid.NewString()}
	for _, id := range ids {
		if err := store.RecordDelivery(ctx, id, ruleID, source.ID, target.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetExtensionApproved(ctx, quotaTestExtension, false); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"RECEIVED", "APPLIED"} {
		if _, err := store.ClaimDeliveryReceipt(ctx, target.ID, ruleID, ids[i], status, now);
			!errors.Is(err, ErrRuleDenied) {
			t.Fatalf("revoked Chrome Origin obtained %s ACK: %v", status, err)
		}
	}
	for _, id := range ids {
		var received int
		if err := store.db.QueryRowContext(ctx, `SELECT received FROM browser_sync_deliveries
			WHERE message_id=?`, id).Scan(&received); err != nil || received != 0 {
			t.Fatalf("revoked Origin mutated pending receipt %s: received=%d err=%v", id, received, err)
		}
	}
	// Ensure no accidental terminal record exists after denied receipt.
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_delivery_results
		WHERE message_id IN (?,?)`, ids[0], ids[1]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked Origin wrote %d terminal results: %v", count, err)
	}
}
