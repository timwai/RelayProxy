package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

const deliveryRetention = 15 * time.Minute

// Delivery receipts contain message metadata only. Ciphertexts, Cookies and
// credential values are never written to this table.
func (s *Store) EnsureDeliverySchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_deliveries (
		message_id TEXT PRIMARY KEY,
		rule_id TEXT NOT NULL,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		expires_at INTEGER NOT NULL,
		received INTEGER NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_browser_sync_delivery_expiry
		ON browser_sync_deliveries(expires_at)`)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_delivery_results (
		message_id TEXT PRIMARY KEY,
		rule_id TEXT NOT NULL,
		source_id TEXT NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('APPLIED','FAILED','CONFLICT')),
		expires_at INTEGER NOT NULL
	)`)
	return err
}

// RecordDelivery establishes a one-time receiver-bound receipt claim BEFORE
// forwarding the encrypted frame, so a fast receiver cannot race the insert.
func (s *Store) RecordDelivery(ctx context.Context, messageID, ruleID, sourceID, targetID string, now time.Time) error {
	if _, err := uuid.Parse(messageID); err != nil {
		return ErrRuleDenied
	}
	if _, err := uuid.Parse(ruleID); err != nil {
		return ErrRuleDenied
	}
	if sourceID == "" || targetID == "" || sourceID == targetID {
		return ErrRuleDenied
	}
	expiry := now.Add(deliveryRetention).Unix()
	_, err := s.db.ExecContext(ctx, `DELETE FROM browser_sync_deliveries WHERE expires_at<=?`, now.Unix())
	if err != nil {
		return err
	}
	// Keep terminal results only for a short recovery window.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM browser_sync_delivery_results WHERE expires_at<=?`, now.Unix()); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO browser_sync_deliveries(
		message_id,rule_id,source_id,target_id,expires_at
	) VALUES(?,?,?,?,?)`, messageID, ruleID, sourceID, targetID, expiry)
	return err
}

func (s *Store) DiscardDelivery(ctx context.Context, messageID string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM browser_sync_deliveries WHERE message_id=?`, messageID)
}

// ClaimDeliveryReceipt requires an active, mutually approved rule AND an
// actual previous encrypted transmission for this exact receiver/message.
// RECEIVED may precede exactly one terminal acknowledgement.
func (s *Store) ClaimDeliveryReceipt(ctx context.Context, receiverID, ruleID, messageID, status string, now time.Time) (string, error) {
	switch status {
	case "RECEIVED", "APPLIED", "FAILED", "CONFLICT":
	default:
		return "", ErrRuleDenied
	}
	receiver, err := s.deviceAllowed(ctx, receiverID)
	if err != nil || !receiver.Receive {
		return "", ErrRuleDenied
	}
	rule, err := s.ActiveRule(ctx, ruleID)
	if err != nil || !rule.Active || !rule.SourceApproved || !rule.TargetApproved ||
		!rule.KeyFingerprintsVerified || rule.TargetBrowserDeviceID != receiver.ID {
		return "", ErrRuleDenied
	}
	source, err := s.deviceAllowed(ctx, rule.SourceBrowserDeviceID)
	if err != nil || !source.Send || source.IdentityID != receiver.IdentityID {
		return "", ErrRuleDenied
	}
	if _, err := uuid.Parse(messageID); err != nil {
		return "", ErrRuleDenied
	}

	// The receipt and its terminal result must commit atomically. If A is
	// temporarily offline, its signed session can still query this outcome.
	if status == "RECEIVED" {
		result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_deliveries SET received=1
			WHERE message_id=? AND rule_id=? AND source_id=? AND target_id=?
			AND expires_at>? AND received=0`,
			messageID, ruleID, source.ID, receiverID, now.Unix())
		if err != nil {
			return "", err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return "", ErrRuleDenied
		}
		return source.ID, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM browser_sync_deliveries
		WHERE message_id=? AND rule_id=? AND source_id=? AND target_id=?
		AND expires_at>?`, messageID, ruleID, source.ID, receiverID, now.Unix())
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return "", ErrRuleDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO browser_sync_delivery_results
		(message_id,rule_id,source_id,status,expires_at) VALUES(?,?,?,?,?)`,
		messageID, ruleID, source.ID, status, now.Add(deliveryRetention).Unix())
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return source.ID, nil
}

func (s *Store) ActiveDeliveryCount(ctx context.Context, ruleID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM browser_sync_deliveries
		WHERE rule_id=? AND expires_at>?`, ruleID, time.Now().Unix()).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}

// DeliveryStatus is readable only by the approved source for this exact
// active rule; a caller cannot enumerate other devices' acknowledgements.
// UNKNOWN intentionally does not imply "Cookie not applied".
func (s *Store) DeliveryStatus(ctx context.Context, sourceID, ruleID, messageID string, now time.Time) (string, error) {
	if _, err := uuid.Parse(messageID); err != nil {
		return "", ErrRuleDenied
	}
	if _, err := s.SessionCursor(ctx, sourceID, ruleID); err != nil {
		return "", ErrRuleDenied
	}
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM browser_sync_delivery_results
		WHERE message_id=? AND rule_id=? AND source_id=? AND expires_at>?`,
		messageID, ruleID, sourceID, now.Unix()).Scan(&status)
	if err == nil {
		return status, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var received int
	err = s.db.QueryRowContext(ctx, `SELECT received FROM browser_sync_deliveries
		WHERE message_id=? AND rule_id=? AND source_id=? AND expires_at>?`,
		messageID, ruleID, sourceID, now.Unix()).Scan(&received)
	if errors.Is(err, sql.ErrNoRows) {
		return "UNKNOWN", nil
	}
	if err != nil {
		return "", err
	}
	if received == 1 {
		return "RECEIVED", nil
	}
	return "PENDING", nil
}
