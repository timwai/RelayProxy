package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

const deliveryRetention = 15 * time.Minute
const maxPendingDeliveriesPerRule = 16
const maxPendingDeliveriesPerSource = 128

// The ACK recovery window must be bounded across many active rules/devices.
// Count both pending deliveries AND terminal results at admission, so later
// ACK transitions cannot exceed the budget or evict an unexpired receipt.
const maxTrackedDeliveriesPerSource = 2048
const maxTrackedDeliveriesPerIdentity = 4096
const maxTrackedDeliveriesGlobal = 32768

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
	if err != nil {
		return err
	}
	for _, query := range []string{
		`CREATE INDEX IF NOT EXISTS idx_browser_sync_delivery_source_expiry
			ON browser_sync_deliveries(source_id,expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_browser_sync_delivery_result_source_expiry
			ON browser_sync_delivery_results(source_id,expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_browser_sync_delivery_result_expiry
			ON browser_sync_delivery_results(expires_at)`,
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM browser_sync_deliveries WHERE expires_at<=?`, now.Unix()); err != nil {
		return err
	}
	// Keep terminal results only for a short recovery window.
	if _, err := tx.ExecContext(ctx, `DELETE FROM browser_sync_delivery_results WHERE expires_at<=?`, now.Unix()); err != nil {
		return err
	}
	// Recheck active pairing, device approval, capabilities and common
	// identity in the SAME transaction as the admission quota. A device
	// revocation or identity change must not bypass accounting.
	var identityID string
	if err := tx.QueryRowContext(ctx, `SELECT src.identity_id
		FROM browser_sync_rules r
		JOIN browser_sync_devices src ON src.id=r.source_id
		JOIN browser_sync_devices dst ON dst.id=r.target_id
		WHERE r.id=? AND r.source_id=? AND r.target_id=? AND r.status='active'
		AND r.source_approved=1 AND r.target_approved=1 AND r.keys_confirmed=1
		AND src.state='approved' AND dst.state='approved'
		AND src.can_send=1 AND dst.can_receive=1
		AND src.identity_id!='' AND src.identity_id=dst.identity_id`,
		ruleID, sourceID, targetID).Scan(&identityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRuleDenied
		}
		return err
	}
	var pendingRule, pendingSource int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_deliveries
		WHERE rule_id=? AND expires_at>?`, ruleID, now.Unix()).Scan(&pendingRule); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_deliveries
		WHERE source_id=? AND expires_at>?`, sourceID, now.Unix()).Scan(&pendingSource); err != nil {
		return err
	}
	if pendingRule >= maxPendingDeliveriesPerRule || pendingSource >= maxPendingDeliveriesPerSource {
		return ErrRuleDenied
	}
	// Every successful terminal ACK removes one pending row and inserts one
	// result row; reserving capacity here guarantees the ACK can be stored
	// without evicting another recoverable terminal result.
	var trackedSource, trackedIdentity, trackedGlobal int
	const tracked = `SELECT source_id FROM browser_sync_deliveries WHERE expires_at>?
		UNION ALL SELECT source_id FROM browser_sync_delivery_results WHERE expires_at>?`
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+tracked+`)
		WHERE source_id=?`, now.Unix(), now.Unix(), sourceID).Scan(&trackedSource); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+tracked+`) d
		JOIN browser_sync_devices src ON src.id=d.source_id
		WHERE src.identity_id=?`, now.Unix(), now.Unix(), identityID).Scan(&trackedIdentity); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+tracked+`)`,
		now.Unix(), now.Unix()).Scan(&trackedGlobal); err != nil {
		return err
	}
	if trackedSource >= maxTrackedDeliveriesPerSource ||
		trackedIdentity >= maxTrackedDeliveriesPerIdentity ||
		trackedGlobal >= maxTrackedDeliveriesGlobal {
		return ErrRuleDenied
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_deliveries(
		message_id,rule_id,source_id,target_id,expires_at
	) VALUES(?,?,?,?,?)`, messageID, ruleID, sourceID, targetID, expiry); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DiscardDelivery(ctx context.Context, messageID string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM browser_sync_deliveries WHERE message_id=?`, messageID)
}

// ClaimDeliveryReceipt requires a mutually approved active/paused rule AND
// an already registered encrypted delivery. A pause must not lose its ACK.
// RECEIVED may precede exactly one terminal acknowledgement.
func (s *Store) ClaimDeliveryReceipt(ctx context.Context, receiverID, ruleID, messageID, status string, now time.Time) (string, error) {
	return s.ClaimDeliveryReceiptWithCode(ctx, receiverID, ruleID, messageID, status, "", now)
}

// ClaimDeliveryReceiptWithCode stores only a fixed diagnostic category, never
// a Chrome exception message, website name or decrypted session content.
func (s *Store) ClaimDeliveryReceiptWithCode(ctx context.Context, receiverID, ruleID, messageID, status, code string, now time.Time) (string, error) {
	if !validBrowserSyncErrorCode(code) || (status == "APPLIED" && code != "") ||
		(status == "RECEIVED" && code != "") {
		return "", ErrRuleDenied
	}
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
	if err != nil || (rule.Status != "active" && rule.Status != "paused") ||
		!rule.SourceApproved || !rule.TargetApproved ||
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
	if err := appendBrowserAuditDetail(ctx, tx, "delivery_result", source.ID, receiverID, ruleID, status, code); err != nil {
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
	// A paused rule no longer permits snapshots, but the source must still
	// be able to recover the terminal ACK for a pre-pause delivery.
	source, err := s.deviceAllowed(ctx, sourceID)
	if err != nil || !source.Send {
		return "", ErrRuleDenied
	}
	rule, err := s.ActiveRule(ctx, ruleID)
	if err != nil || (rule.Status != "active" && rule.Status != "paused") ||
		!rule.SourceApproved || !rule.TargetApproved || !rule.KeyFingerprintsVerified ||
		rule.SourceBrowserDeviceID != sourceID {
		return "", ErrRuleDenied
	}
	target, err := s.deviceAllowed(ctx, rule.TargetBrowserDeviceID)
	if err != nil || !target.Receive || target.IdentityID != source.IdentityID {
		return "", ErrRuleDenied
	}
	var status string
	err = s.db.QueryRowContext(ctx, `SELECT status FROM browser_sync_delivery_results
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
