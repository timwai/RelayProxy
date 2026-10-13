package browsersync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Seed rows in one SQL statement rather than 32K individual transactions.
// These mimic receipts from other rules/devices without storing credentials.
func seedTrackedReceipts(t *testing.T, store *Store, prefix, ruleID, sourceID string, count int, expiry time.Time) {
	t.Helper()
	_, err := store.db.ExecContext(context.Background(), `WITH RECURSIVE ids(n) AS (
		SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<?
	) INSERT INTO browser_sync_delivery_results
		(message_id,rule_id,source_id,status,expires_at)
		SELECT ?||n,?,?, 'APPLIED',? FROM ids`,
		count, prefix, ruleID, sourceID, expiry.Unix())
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryReceiptRecoveryWindowHasPersistentQuotas(t *testing.T) {
	t.Run("per source reserves capacity for ACK and recovers on expiry", func(t *testing.T) {
		store := testBrowserStore(t)
		source, target, ruleID := approvedDeliveryRule(t, store)
		ctx, now := context.Background(), time.Now().UTC()
		seedTrackedReceipts(t, store, "source-", ruleID, source.ID,
			maxTrackedDeliveriesPerSource, now.Add(deliveryRetention))
		msg := uuid.NewString()
		if err := store.RecordDelivery(ctx, msg, ruleID, source.ID, target.ID, now); !errors.Is(err, ErrRuleDenied) {
			t.Fatalf("per-source retained ACK budget bypassed: %v", err)
		}
		var before int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM browser_sync_delivery_results
			WHERE source_id=?`, source.ID).Scan(&before); err != nil || before != maxTrackedDeliveriesPerSource {
			t.Fatalf("an admission failure lost recoverable ACKs: count=%d err=%v", before, err)
		}
		later := now.Add(deliveryRetention + time.Second)
		if err := store.RecordDelivery(ctx, msg, ruleID, source.ID, target.ID, later); err != nil {
			t.Fatalf("expired terminal records still block new delivery: %v", err)
		}
		if _, err := store.ClaimDeliveryReceipt(ctx, target.ID, ruleID, msg, "APPLIED", later); err != nil {
			t.Fatalf("quota did not reserve room for terminal ACK: %v", err)
		}
		status, err := store.DeliveryStatus(ctx, source.ID, ruleID, msg, later)
		if err != nil || status != "APPLIED" {
			t.Fatalf("unexpired terminal receipt not recoverable: %q %v", status, err)
		}
	})

	t.Run("per identity counts all approved devices, not only one source", func(t *testing.T) {
		store := testBrowserStore(t)
		source, target, ruleID := approvedDeliveryRule(t, store)
		now := time.Now().UTC()
		// Both devices belong to id_1. Keep the source below its own limit
		// while consuming the shared identity budget through its peer.
		seedTrackedReceipts(t, store, "identity-", ruleID, target.ID,
			maxTrackedDeliveriesPerIdentity, now.Add(deliveryRetention))
		if err := store.RecordDelivery(context.Background(), uuid.NewString(),
			ruleID, source.ID, target.ID, now); !errors.Is(err, ErrRuleDenied) {
			t.Fatalf("per-identity retained receipt budget bypassed: %v", err)
		}
	})

	t.Run("global cap covers retained results from many identities", func(t *testing.T) {
		store := testBrowserStore(t)
		source, target, ruleID := approvedDeliveryRule(t, store)
		now := time.Now().UTC()
		// Even orphaned historical records count against the global cap.
		seedTrackedReceipts(t, store, "global-", ruleID, "orphaned-device",
			maxTrackedDeliveriesGlobal, now.Add(deliveryRetention))
		if err := store.RecordDelivery(context.Background(), uuid.NewString(),
			ruleID, source.ID, target.ID, now); !errors.Is(err, ErrRuleDenied) {
			t.Fatalf("global retained receipt budget bypassed: %v", err)
		}
	})
}

func TestDeliverySchemaQuotaIndexesAreIdempotent(t *testing.T) {
	store := testBrowserStore(t)
	if err := store.EnsureDeliverySchema(context.Background()); err != nil {
		t.Fatalf("existing SQLite database migration failed: %v", err)
	}
	for _, name := range []string{
		"idx_browser_sync_delivery_source_expiry",
		"idx_browser_sync_delivery_result_source_expiry",
		"idx_browser_sync_delivery_result_expiry",
	} {
		var count int
		if err := store.db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("quota index %q missing: %v (%d)", name, err, count)
		}
	}
}
