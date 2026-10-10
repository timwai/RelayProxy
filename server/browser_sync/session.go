package browsersync

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math"
	"math/big"
	"time"

	protocol "relayproxy/internal/browser_sync"
)

var ErrReplay = errors.New("browser session sequence is stale or duplicate")

// Session transport never receives or parses the plaintext Cookie payload.
// An authenticated sender also signs the entire AEAD envelope.
func (s *Store) ValidateSignedSession(ctx context.Context, senderID string, e protocol.Envelope, now time.Time) error {
	if e.Type != protocol.SessionSnapshot {
		return errors.New("only full encrypted session snapshots are supported")
	}
	if err := protocol.ValidateEnvelope(e, now); err != nil {
		return err
	}
	sender, err := s.deviceAllowed(ctx, senderID)
	if err != nil || sender.ID != e.SourceBrowserDeviceID || !sender.Send {
		return ErrRuleDenied
	}
	receiver, err := s.deviceAllowed(ctx, e.TargetBrowserDeviceID)
	if err != nil || !receiver.Receive || receiver.IdentityID != sender.IdentityID {
		return ErrRuleDenied
	}
	rule, err := s.ActiveRule(ctx, e.RuleID)
	if err != nil {
		return err
	}
	if err := AuthorizeRelay(
		VerifiedDevice{ID: sender.ID, Approved: true, Send: sender.Send},
		VerifiedDevice{ID: receiver.ID, Approved: true, Receive: receiver.Receive},
		rule, e,
	); err != nil {
		return ErrRuleDenied
	}
	key, err := validateP256SPKI(sender.SigningKey)
	if err != nil {
		return errors.New("browser sender key invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(e.Signature)
	if err != nil || len(raw) != 64 {
		return errors.New("browser snapshot signature invalid")
	}
	signatureHash := sha256.Sum256([]byte(protocol.SignedHeader(e) + "\n" + e.Ciphertext))
	if !ecdsa.Verify(key, signatureHash[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])) {
		return errors.New("browser snapshot signature check failed")
	}
	return nil
}

// AdvanceSessionSequence is a durable monotonic replay gate; it must be
// called immediately before relaying a verified envelope to the recipient.
// A rejected delivery still consumes the sequence to prevent replay.
func (s *Store) AdvanceSessionSequence(ctx context.Context, ruleID string, seq uint64) error {
	if seq == 0 || seq > math.MaxInt64 {
		return ErrReplay
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO browser_sync_sequences(rule_id,last_sequence)
		VALUES(?,?) ON CONFLICT(rule_id) DO UPDATE SET last_sequence=excluded.last_sequence
		WHERE browser_sync_sequences.last_sequence<excluded.last_sequence`, ruleID, int64(seq))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrReplay
	}
	return nil
}
func (s *Store) EnsureSessionSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_sequences (
		rule_id TEXT PRIMARY KEY REFERENCES browser_sync_rules(id) ON DELETE CASCADE,
		last_sequence INTEGER NOT NULL CHECK(last_sequence>0)
	)`)
	return err
}
