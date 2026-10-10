package browsersync

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func registerBrowserForRules(t *testing.T, store *Store, id string, send, receive bool) {
	t.Helper()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	device := BrowserDevice{ID: id, Name: "Rule test", SigningKey: signing, EncryptionKey: encryption}
	if _, err := store.Register(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(context.Background(), id, "id_1", send, receive); err != nil {
		t.Fatal(err)
	}
}

func encryptedTestOffer(id, target string) EncryptedOffer {
	digest := sha256.Sum256([]byte("https://example.com"))
	encode := func(size int) string {
		return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", size)))
	}
	return EncryptedOffer{
		RuleID: id, TargetID: target,
		PolicyDigest: hex.EncodeToString(digest[:]),
		EphemeralKey: encode(91), Salt: encode(32), IV: encode(12),
		Ciphertext: encode(60), Signature: encode(64),
	}
}

func TestRuleRequiresTwoIndependentApprovals(t *testing.T) {
	store := testBrowserStore(t)
	if err := store.EnsureRuleSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := "browser_" + uuid.NewString()
	target := "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, source, true, false)
	registerBrowserForRules(t, store, target, false, true)
	id := uuid.NewString()
	offer := encryptedTestOffer(id, target)
	if err := store.OfferRule(context.Background(), source, offer); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(context.Background(), source, id); !errors.Is(err, ErrRuleDenied) {
		t.Fatal("source activated rule without target consent")
	}
	if err := store.AcceptRule(context.Background(), source, id); !errors.Is(err, ErrRuleDenied) {
		t.Fatal("source impersonated receiver")
	}
	rules, err := store.ListRules(context.Background(), target)
	if err != nil || len(rules) != 1 || rules[0].TargetApproved {
		t.Fatalf("offer missing or prematurely accepted: %+v %v", rules, err)
	}
	if err := store.AcceptRule(context.Background(), target, id); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(context.Background(), source, id); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveRule(context.Background(), id)
	if err != nil || !active.Active || !active.KeyFingerprintsVerified {
		t.Fatalf("expected active rule: %+v %v", active, err)
	}
	if err := store.RevokeRule(context.Background(), target, id); err != nil {
		t.Fatal(err)
	}
	active, _ = store.ActiveRule(context.Background(), id)
	if active.Active {
		t.Fatal("revoked rule remained active")
	}
}

func TestRuleNoCrossIdentityOrDuplicate(t *testing.T) {
	store := testBrowserStore(t)
	if err := store.EnsureRuleSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	source := "browser_" + uuid.NewString()
	target := "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, source, true, false)
	_, sign := makeKey(t)
	_, enc := makeKey(t)
	if _, err := store.Register(context.Background(), BrowserDevice{
		ID: target, Name: "Unapproved receiver", SigningKey: sign, EncryptionKey: enc,
	}); err != nil {
		t.Fatal(err)
	}
	offer := encryptedTestOffer(uuid.NewString(), target)
	if err := store.OfferRule(context.Background(), source, offer); !errors.Is(err, ErrRuleDenied) {
		t.Fatal("pending target was permitted")
	}
	if err := store.Approve(context.Background(), target, "id_1", false, true); err != nil {
		t.Fatal(err)
	}
	if err := store.OfferRule(context.Background(), source, offer); err != nil {
		t.Fatal(err)
	}
	if err := store.OfferRule(context.Background(), source, offer); !errors.Is(err, ErrRuleDenied) {
		t.Fatal("duplicate ID was reused")
	}
	if err := store.AcceptRule(context.Background(), target, "invalid"); !errors.Is(err, ErrRuleDenied) {
		t.Fatal("invalid rule id was accepted")
	}
}

func TestRuleQuotaCountsIncomingAndReleasesRevoked(t *testing.T) {
	store := testBrowserStore(t)
	source := "browser_" + uuid.NewString()
	target := "browser_" + uuid.NewString()
	registerBrowserForRules(t, store, source, true, false)
	registerBrowserForRules(t, store, target, false, true)
	var first string
	for i := 0; i < maxBrowserRulesPerDevice; i++ {
		id := uuid.NewString()
		if i == 0 {
			first = id
		}
		if err := store.OfferRule(context.Background(), source, encryptedTestOffer(id, target)); err != nil {
			t.Fatalf("rule %d rejected early: %v", i, err)
		}
	}
	if err := store.OfferRule(context.Background(), source, encryptedTestOffer(uuid.NewString(), target)); !errors.Is(err, ErrRuleDenied) {
		t.Fatalf("over quota offer accepted: %v", err)
	}
	if err := store.RevokeRule(context.Background(), target, first); err != nil {
		t.Fatal(err)
	}
	if err := store.OfferRule(context.Background(), source, encryptedTestOffer(uuid.NewString(), target)); err != nil {
		t.Fatalf("revoked slot not freed: %v", err)
	}
}
