package browsersync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A malicious re-enrollment needs only public keys; it must not rewrite an
// identity choice already recorded by the real device.
func TestEnrollmentCannotRewritePendingIdentityChoice(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	d := BrowserDevice{
		ID:                  "browser_" + uuid.NewString(),
		Name:                "Chrome profile",
		SigningKey:          signing,
		EncryptionKey:       encryption,
		RequestedIdentityID: "id_1",
	}
	if _, _, err := store.EnrollExtensionDevice(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", d); err != nil {
		t.Fatal(err)
	}
	replay := d
	replay.RequestedIdentityID = "id_attacker"
	if _, _, err := store.EnrollExtensionDevice(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", replay); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Device(ctx, d.ID)
	if err != nil || saved.RequestedIdentityID != "id_1" || saved.IdentityID != "" || saved.State != "pending" {
		t.Fatalf("unverified re-enrollment changed pending identity: %+v %v", saved, err)
	}
}

func TestOriginInventoryExistingRegistrationSurvivesQuota(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	first := ""
	for i := 0; i < 500; i++ {
		id := []byte(strings.Repeat("a", 32))
		id[29] = byte('a' + i/(16*16))
		id[30] = byte('a' + (i/16)%16)
		id[31] = byte('a' + i%16)
		if i == 0 {
			first = string(id)
		}
		if _, err := store.RequestExtension(ctx, string(id)); err != nil {
			t.Fatalf("origin %d: %v", i, err)
		}
	}
	if got, err := store.RequestExtension(ctx, first); err != nil || got != "approved" {
		t.Fatalf("existing registered origin failed at full inventory: %q %v", got, err)
	}
	if _, err := store.RequestExtension(ctx, "pppppppppppppppppppppppppppppppp"); err == nil {
		t.Fatal("new unbounded Origin allowed beyond inventory quota")
	}
}

func TestOriginAuthorizationFailsClosedWhenDatabaseUnavailable(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, nil)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	allowed, err := store.ExtensionOriginAllowed(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if allowed || err == nil {
		t.Fatalf("origin permitted when database unavailable: %v %v", allowed, err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://relay.example.com/api/v1/browser-sync/devices/register", strings.NewReader("{}"))
	req.Header.Set("Origin", "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail-open registration status: %d", resp.Code)
	}
}

func TestRevocationAndPairingAreAtomic(t *testing.T) {
	store := testBrowserStore(t)
	ctx := context.Background()
	src := "browser_" + uuid.NewString()
	dst := "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, src, true, false)
	registerBrowserForRules(t, store, dst, false, true)
	ruleID := uuid.NewString()
	if err := store.OfferRule(ctx, src, encryptedTestOffer(ruleID, dst)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER deny_revoke_rule BEFORE UPDATE ON browser_sync_rules BEGIN SELECT RAISE(ABORT, 'test rule failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, src); err == nil {
		t.Fatal("expected simulated database error")
	}
	device, err := store.Device(ctx, src)
	if err != nil || device.State != "approved" || !device.Send {
		t.Fatalf("revocation partially committed: %+v %v", device, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER deny_revoke_rule`); err != nil {
		t.Fatal(err)
	}
	if err := store.Revoke(ctx, src); err != nil {
		t.Fatal(err)
	}
	device, err = store.Device(ctx, src)
	if err != nil || device.State != "revoked" || device.Send {
		t.Fatalf("revocation did not commit: %+v %v", device, err)
	}
	active, err := store.ActiveRule(ctx, ruleID)
	if err != nil && !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("rule query: %v", err)
	}
	if err == nil && active.Active {
		t.Fatal("active rule survived device revocation")
	}
}
