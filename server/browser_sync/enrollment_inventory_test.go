package browsersync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

const quotaTestExtension = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const quotaOtherExtension = "cccccccccccccccccccccccccccccccc"

func quotaBrowserIdentity(t *testing.T) BrowserDevice {
	t.Helper()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	return BrowserDevice{
		ID: "browser_" + uuid.NewString(), Name: "pending inventory",
		SigningKey: signing, EncryptionKey: encryption,
		RequestedIdentityID: "id_1",
	}
}

// Populate a realistic historical pending backlog in a single transaction.
// The fake keys are never trusted or approved; only quota counts are exercised.
func seedPendingInventory(t *testing.T, store *Store, n int, origin string) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ids := make([]string, n)
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		id := "browser_" + uuid.NewString()
		ids[i] = id
		_, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_devices
			(id,name,signing_key,encryption_key,created_at,updated_at)
			VALUES(?,?,?,?,?,?)`, id, "old pending",
			fmt.Sprintf("pending-sign-%s", id), fmt.Sprintf("pending-enc-%s", id), now, now)
		if err != nil {
			t.Fatal(err)
		}
		if origin != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_device_extensions
				(device_id,extension_id,created_at) VALUES(?,?,?)`,
				id, origin, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestGlobalPendingInventoryPreservesExistingEnrollmentAndReleasedCapacity(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	// A pre-existing record must be usable when the inventory becomes full.
	existing := quotaBrowserIdentity(t)
	if _, err := store.Register(ctx, existing); err != nil {
		t.Fatal(err)
	}
	seedPendingInventory(t, store, maxPendingBrowserEnrollmentsGlobal-1, "")
	newDevice := quotaBrowserIdentity(t)
	if _, err := store.Register(ctx, newDevice); !errors.Is(err, ErrBrowserEnrollmentQuota) {
		t.Fatalf("global pending inventory exceeded: %v", err)
	}
	if _, err := store.Device(ctx, newDevice.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected registration left an orphaned device: %v", err)
	}
	existing.Name = "attacker-changed-name"
	existing.RequestedIdentityID = "different-untrusted-identity"
	got, err := store.Register(ctx, existing)
	if err != nil || got.Name != "pending inventory" || got.RequestedIdentityID != "id_1" {
		t.Fatalf("existing device could not safely re-register at full quota: %+v %v", got, err)
	}
	existing.SigningKey = "different-key"
	if _, err := store.Register(ctx, existing); !errors.Is(err, ErrDeviceConflict) {
		t.Fatalf("changed public key accepted at full quota: %v", err)
	}
	if err := store.Revoke(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(ctx, newDevice); err != nil {
		t.Fatalf("revoking a pending record did not free capacity: %v", err)
	}
}

func TestPendingInventoryOriginQuotaAndAtomicBinding(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	for _, ext := range []string{quotaTestExtension, quotaOtherExtension} {
		if _, err := store.RequestExtension(ctx, ext); err != nil {
			t.Fatal(err)
		}
	}
	existing := quotaBrowserIdentity(t)
	if _, _, err := store.EnrollExtensionDevice(ctx, quotaTestExtension, existing); err != nil {
		t.Fatal(err)
	}
	seedPendingInventory(t, store, maxPendingBrowserEnrollmentsPerExtension-1, quotaTestExtension)
	blocked := quotaBrowserIdentity(t)
	if _, _, err := store.EnrollExtensionDevice(ctx, quotaTestExtension, blocked); !errors.Is(err, ErrBrowserEnrollmentQuota) {
		t.Fatalf("extension Origin exceeded pending budget: %v", err)
	}
	if _, err := store.Device(ctx, blocked.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed origin quota left an orphan: %v", err)
	}
	if got, _, err := store.EnrollExtensionDevice(ctx, quotaTestExtension, existing); err != nil ||
		got.ExtensionID != quotaTestExtension {
		t.Fatalf("existing bound device blocked by origin quota: %+v %v", got, err)
	}
	if _, _, err := store.EnrollExtensionDevice(ctx, quotaOtherExtension, blocked); err != nil {
		t.Fatalf("different Origin rejected before global inventory fills: %v", err)
	}
	if _, _, err := store.EnrollExtensionDevice(ctx, quotaTestExtension, blocked); !errors.Is(err, ErrDeviceConflict) {
		t.Fatalf("re-enrollment changed pin to another extension: %v", err)
	}
	if err := store.Revoke(ctx, existing.ID); err != nil {
		t.Fatal(err)
	}
	newForOrigin := quotaBrowserIdentity(t)
	if _, _, err := store.EnrollExtensionDevice(ctx, quotaTestExtension, newForOrigin); err != nil {
		t.Fatalf("revoked pending Origin enrollment did not free its slot: %v", err)
	}
}

func TestPendingInventoryHTTP429DoesNotApproveOrSavePublicKeys(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	if _, err := store.RequestExtension(ctx, quotaTestExtension); err != nil {
		t.Fatal(err)
	}
	seedPendingInventory(t, store, maxPendingBrowserEnrollmentsPerExtension, quotaTestExtension)
	device := quotaBrowserIdentity(t)
	body, err := json.Marshal(device)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(store, nil)
	for _, path := range []string{
		"/api/v1/browser-sync/devices/register",
		"/api/v1/browser-sync/extensions/register",
	} {
		req := httptest.NewRequest(http.MethodPost, "https://relay.example.com"+path, bytes.NewReader(body))
		req.Header.Set("Origin", "chrome-extension://"+quotaTestExtension)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.27:45721"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s quota should return HTTP 429, got %d: %s", path, rec.Code, rec.Body.String())
		}
		if _, err := store.Device(ctx, device.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s quota stored unauthorized device: %v", path, err)
		}
	}
}

func TestPendingInventorySchemaUpgradeIndexesRemainIdempotent(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	if err := store.EnsureExtensionSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"idx_browser_pending_device_inventory",
		"idx_browser_device_extension_inventory",
	} {
		var count int
		err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type='index' AND name=?`, name).Scan(&count)
		if err != nil || count != 1 {
			t.Fatalf("inventory index %q not initialized: count=%d err=%v", name, count, err)
		}
	}
}
