package browsersync

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	protocol "relayproxy/internal/browser_sync"
)

func signedTestSnapshot(t *testing.T, signing *ecdsa.PrivateKey, source, target, ruleID string) protocol.Envelope {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	encoded := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	e := protocol.Envelope{
		Protocol: protocol.ProtocolVersion, Type: protocol.SessionSnapshot,
		MessageID: uuid.NewString(), RuleID: ruleID,
		SourceBrowserDeviceID: source, TargetBrowserDeviceID: target,
		Sequence: 1, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		Encryption: protocol.EncryptionHeader{
			Suite: protocol.SessionCipherSuite, KeyID: target,
			Enc:  encoded([]byte("test P256 public key bytes")),
			Salt: encoded(make([]byte, 32)), IV: encoded(make([]byte, 12)),
		},
		Ciphertext: encoded([]byte("end-to-end encrypted data only")),
	}
	digest := sha256.Sum256([]byte(protocol.SignedHeader(e) + "\n" + e.Ciphertext))
	r, s, err := ecdsa.Sign(rand.Reader, signing, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	p1363 := make([]byte, 64)
	r.FillBytes(p1363[:32])
	s.FillBytes(p1363[32:])
	e.Signature = encoded(p1363)
	return e
}

func TestSignedBrowserSessionAndDurableReplay(t *testing.T) {
	store := testBrowserStore(t)
	source, key := approvedWSSBrowser(t, store, true, false)
	target, _ := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	if err := store.OfferRule(context.Background(), source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(context.Background(), target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(context.Background(), source.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	e := signedTestSnapshot(t, key, source.ID, target.ID, ruleID)
	ctx := context.Background()
	if err := store.ValidateSignedSession(ctx, source.ID, e, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceSessionSequence(ctx, ruleID, e.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceSessionSequence(ctx, ruleID, e.Sequence); err == nil {
		t.Fatal("duplicate sequence accepted")
	}
	if err := store.AdvanceSessionSequence(ctx, ruleID, e.Sequence-1); err == nil {
		t.Fatal("older sequence accepted")
	}
	tampered := e
	tampered.Ciphertext = base64.RawURLEncoding.EncodeToString([]byte("ciphertext replaced"))
	if err := store.ValidateSignedSession(ctx, source.ID, tampered, time.Now().UTC()); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	tampered = e
	tampered.SourceBrowserDeviceID = "browser_" + uuid.NewString()
	if err := store.ValidateSignedSession(ctx, source.ID, tampered, time.Now().UTC()); err == nil {
		t.Fatal("spoofed sender accepted")
	}
	if err := store.RevokeRule(ctx, target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateSignedSession(ctx, source.ID, e, time.Now().UTC()); err == nil {
		t.Fatal("revoked rule accepted")
	}
}

func TestSignedHeaderStable(t *testing.T) {
	now := time.Date(2026, 10, 10, 14, 0, 0, 123000000, time.UTC)
	e := protocol.Envelope{Protocol: "browser.sync.v1", Type: protocol.SessionSnapshot,
		MessageID: "msg", RuleID: "rule", SourceBrowserDeviceID: "a", TargetBrowserDeviceID: "b",
		Sequence: 7, CreatedAt: now, ExpiresAt: now.Add(time.Minute),
		Encryption: protocol.EncryptionHeader{Suite: protocol.SessionCipherSuite, KeyID: "target", Enc: "pub", Salt: "salt", IV: "iv"}}
	expected := "browser.sync.v1\nSESSION_SNAPSHOT\nmsg\nrule\na\nb\n7\n1791640800123\n1791640860123\n" +
		protocol.SessionCipherSuite + "\ntarget\npub\nsalt\niv"
	// SignedHeader is independent of RFC3339 fractional-second formatting.
	if got := protocol.SignedHeader(e); got != expected {
		t.Fatalf("header mismatch: got %q want %q", got, expected)
	}
	_ = new(big.Int) // keep this test independent of production sign code
}
