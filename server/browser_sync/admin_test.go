package browsersync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func adminTestPair(t *testing.T, store *Store) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	source, target := "browser_" + uuid.NewString(), "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, source, true, false)
	registerBrowserForRules(t, store, target, false, true)
	id := uuid.NewString()
	if err := store.OfferRule(ctx, source, encryptedTestOffer(id, target)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(ctx, target, id); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(ctx, source, id); err != nil {
		t.Fatal(err)
	}
	return source, target, id
}

func TestBrowserAdminRulesNeverExposeEncryptedOfferAndRequireSafeTransitions(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	_, target, ruleID := adminTestPair(t, store)
	rules, err := store.AdminRules(ctx)
	if err != nil || len(rules) != 1 || rules[0].Status != "active" ||
		!rules[0].SourceApproved || !rules[0].TargetApproved || !rules[0].KeysConfirmed {
		t.Fatalf("unexpected metadata rules: %+v %v", rules, err)
	}
	encoded, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, secretField := range []string{"offer", "ciphertext", "policyDigest", "signature", "signingKey", "encryptionKey"} {
		if strings.Contains(string(encoded), secretField) {
			t.Fatalf("browser admin leaks encrypted offer field %q", secretField)
		}
	}
	if err := store.AdminRuleAction(ctx, ruleID, "pause"); err != nil {
		t.Fatal(err)
	}
	if active, err := store.ActiveRule(ctx, ruleID); err != nil || active.Active {
		t.Fatalf("paused rule still active: %+v %v", active, err)
	}
	if err := store.AdminRuleAction(ctx, ruleID, "pause"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("double pause accepted: %v", err)
	}
	if err := store.AdminRuleAction(ctx, ruleID, "resume"); err != nil {
		t.Fatal(err)
	}
	if err := store.AdminRuleAction(ctx, ruleID, "pause"); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := store.AdminRuleAction(ctx, ruleID, "resume"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("revoked device pair resumed: %v", err)
	}
	if err := store.AdminRuleAction(ctx, ruleID, "revoke"); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("device revocation did not revoke pair: %v", err)
	}
}

func TestAdminRulesPageFiltersBeforePagination(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	_, _, firstID := adminTestPair(t, store)
	_, _, secondID := adminTestPair(t, store)
	if err := store.AdminRuleAction(ctx, firstID, "pause"); err != nil {
		t.Fatal(err)
	}
	paused, err := store.AdminRulesPage(ctx, 1, 0, "id_1", "paused", "")
	if err != nil || len(paused) != 1 || paused[0].ID != firstID {
		t.Fatalf("filtered paused rule missing: %+v %v", paused, err)
	}
	empty, err := store.AdminRulesPage(ctx, 10, 0, "id_unknown", "active", "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("identity isolation filter failed: %+v %v", empty, err)
	}
	page1, err := store.AdminRulesPage(ctx, 1, 0, "id_1", "", "")
	if err != nil || len(page1) != 1 {
		t.Fatalf("first page: %+v %v", page1, err)
	}
	page2, err := store.AdminRulesPage(ctx, 1, 1, "id_1", "", "")
	if err != nil || len(page2) != 1 || page2[0].ID == page1[0].ID {
		t.Fatalf("second page overlap: %+v %v", page2, err)
	}
	found, err := store.AdminRulesPage(ctx, 10, 0, "", "", secondID)
	if err != nil || len(found) != 1 || found[0].ID != secondID {
		t.Fatalf("rule ID search failed: %+v %v", found, err)
	}
	for _, args := range []struct {
		limit  int
		offset int
		status string
	}{
		{0, 0, ""}, {101, 0, ""}, {10, -1, ""}, {10, 0, "promoted"},
	} {
		if _, err := store.AdminRulesPage(ctx, args.limit, args.offset, "", args.status, ""); err == nil {
			t.Fatalf("invalid pagination accepted: %+v", args)
		}
	}
}

func TestBrowserAuditTracksActorAndMigratesLegacySchema(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `DROP TABLE browser_sync_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE browser_sync_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		device_id TEXT NOT NULL DEFAULT '',
		peer_id TEXT NOT NULL DEFAULT '',
		rule_id TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureAdminSchema(ctx); err != nil {
		t.Fatalf("failed legacy audit migration: %v", err)
	}
	if err := store.EnsureAdminSchema(ctx); err != nil {
		t.Fatalf("audit migration was not idempotent: %v", err)
	}
	deviceID := "browser_" + uuid.NewString()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	if _, err := store.Register(ctx, BrowserDevice{
		ID: deviceID, Name: "Audit actor", SigningKey: signing, EncryptionKey: encryption,
	}); err != nil {
		t.Fatal(err)
	}
	actorID := "admin-review-001"
	if err := store.Approve(WithAdminActor(ctx, actorID), deviceID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	events, err := store.AdminAudit(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "device_approved" ||
		events[0].ActorID != actorID {
		t.Fatalf("admin actor was lost during audit migration: %+v %v", events, err)
	}
}

func TestBrowserAuditStoresOnlyBoundedTerminalMetadataAndPaginates(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	source, target, ruleID := adminTestPair(t, store)
	now := time.Now().UTC()
	for _, status := range []string{"APPLIED", "FAILED", "CONFLICT"} {
		mid := uuid.NewString()
		if err := store.RecordDelivery(ctx, mid, ruleID, source, target, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimDeliveryReceipt(ctx, target, ruleID, mid, status, now); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.AdminAudit(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var deliveryCount int
	for _, e := range all {
		if e.Type == "delivery_result" {
			deliveryCount++
			if e.DeviceID != source || e.PeerID != target || e.RuleID != ruleID {
				t.Fatalf("audit exposes unrelated data: %+v", e)
			}
			switch e.Outcome {
			case "APPLIED", "FAILED", "CONFLICT":
			default:
				t.Fatal(e)
			}
		}
	}
	if deliveryCount != 3 {
		t.Fatalf("expected 3 terminal results; got %d", deliveryCount)
	}
	filtered, err := store.AdminAuditFiltered(ctx, 0, 50, "delivery_result", source)
	if err != nil || len(filtered) != 3 {
		t.Fatalf("server-side audit filter mismatched: %+v %v", filtered, err)
	}
	if _, err := store.AdminAuditFiltered(ctx, 0, 50, "cookies", ""); err == nil {
		t.Fatal("unapproved audit event filter accepted")
	}
	if _, err := store.AdminAuditFiltered(ctx, 0, 50, "", "invalid_device"); err == nil {
		t.Fatal("invalid audit device filter accepted")
	}
	first, err := store.AdminAudit(ctx, 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	next, err := store.AdminAudit(ctx, first[1].ID, 2)
	if err != nil || len(next) != 2 || next[0].ID >= first[1].ID {
		t.Fatalf("audit cursor overlaps: %+v %v", next, err)
	}
	if _, err := store.AdminAudit(ctx, -1, 50); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if _, err := store.AdminAudit(ctx, 0, 101); err == nil {
		t.Fatal("overlarge page accepted")
	}
	stats, err := store.AdminDeviceStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, d := range stats {
		if d.DeviceID == source || d.DeviceID == target {
			seen++
			if d.Applied != 1 || d.Failed != 1 || d.Conflicted != 1 || d.LastSyncAt == nil {
				t.Fatalf("invalid activity for %s: %+v", d.DeviceID, d)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("did not aggregate both sender and receiver: %d", seen)
	}
}
