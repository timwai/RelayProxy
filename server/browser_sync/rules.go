package browsersync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrRuleDenied = errors.New("browser sync rule denied")
const maxBrowserRulesPerDevice = 32
var policyDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// EncryptedOffer is an opaque, signed browser-to-browser policy proposal.
// Site names and Cookie names are encrypted by the source extension and are
// never interpreted or logged by the Server.
type EncryptedOffer struct {
	RuleID       string `json:"ruleId"`
	TargetID     string `json:"targetBrowserDeviceId"`
	PolicyDigest string `json:"policyDigest"`
	EphemeralKey string `json:"ephemeralKey"`
	Salt         string `json:"salt"`
	IV           string `json:"iv"`
	Ciphertext   string `json:"ciphertext"`
	Signature    string `json:"signature"`
}

// BrowserRule intentionally omits cookie values, token values and cleartext
// site names. It is safe to return to either authorized participating device.
type BrowserRule struct {
	ID             string         `json:"ruleId"`
	SourceID       string         `json:"sourceBrowserDeviceId"`
	TargetID       string         `json:"targetBrowserDeviceId"`
	Status         string         `json:"status"`
	SourceApproved bool           `json:"sourceApproved"`
	TargetApproved bool           `json:"targetApproved"`
	KeysConfirmed  bool           `json:"keysConfirmed"`
	Offer          EncryptedOffer `json:"offer"`
}

func validateOffer(o EncryptedOffer) error {
	if _, err := uuid.Parse(o.RuleID); err != nil {
		return ErrRuleDenied
	}
	if !deviceIDPattern.MatchString(o.TargetID) || !policyDigestPattern.MatchString(o.PolicyDigest) {
		return ErrRuleDenied
	}
	for _, field := range []struct {
		value  string
		minLen int
		maxLen int
	}{{o.EphemeralKey, 50, 600}, {o.Salt, 32, 64},
		{o.IV, 16, 32}, {o.Ciphertext, 24, 8192}, {o.Signature, 80, 100}} {
		if len(field.value) < field.minLen || len(field.value) > field.maxLen {
			return ErrRuleDenied
		}
		if _, err := base64.RawURLEncoding.DecodeString(field.value); err != nil {
			return ErrRuleDenied
		}
	}
	return nil
}

// EnsureRuleSchema is additive to the standalone browser tables created by
// NewStore, so an early development database remains usable.
func (s *Store) EnsureRuleSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_rule_offers (
		rule_id TEXT PRIMARY KEY REFERENCES browser_sync_rules(id) ON DELETE CASCADE,
		body TEXT NOT NULL
	)`)
	return err
}

func (s *Store) deviceAllowed(ctx context.Context, id string) (BrowserDevice, error) {
	d, err := s.Device(ctx, id)
	if err != nil || d.State != "approved" || d.IdentityID == "" {
		return BrowserDevice{}, ErrRuleDenied
	}
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM identities WHERE id=?`, d.IdentityID).Scan(&state); err != nil || state != "active" {
		return BrowserDevice{}, ErrRuleDenied
	}
	return d, nil
}

// OfferRule requires two independently approved browser devices in the same
// identity, with the appropriate send/receive capabilities. It stores only
// encrypted policy data and is idempotent; IDs cannot be reassigned.
func (s *Store) OfferRule(ctx context.Context, sourceID string, offer EncryptedOffer) error {
	if err := validateOffer(offer); err != nil || sourceID == offer.TargetID {
		return ErrRuleDenied
	}
	source, err := s.deviceAllowed(ctx, sourceID)
	if err != nil || !source.Send {
		return ErrRuleDenied
	}
	target, err := s.deviceAllowed(ctx, offer.TargetID)
	if err != nil || !target.Receive || target.IdentityID != source.IdentityID {
		return ErrRuleDenied
	}
	// Require the digest to be exactly 32 bytes in canonical hex, even though
	// decrypting/verifying the policy digest remains the target's job.
	if raw, err := hex.DecodeString(offer.PolicyDigest); err != nil || len(raw) != sha256.Size {
		return ErrRuleDenied
	}
	body, err := json.Marshal(offer)
	if err != nil || len(body) > 12*1024 {
		return ErrRuleDenied
	}
	// A single transaction prevents an orphaned rule without an encrypted body.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Include both sent and received non-revoked rules. Count and insert in
	// the same transaction to avoid bypassing the quota with concurrent offers.
	for _, deviceID := range []string{sourceID, offer.TargetID} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_rules
			WHERE status!='revoked' AND (source_id=? OR target_id=?)`, deviceID, deviceID).Scan(&count); err != nil {
			return err
		}
		if count >= maxBrowserRulesPerDevice { return ErrRuleDenied }
	}
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_rules (
		id,source_id,target_id,policy_digest,status,source_approved,target_approved,keys_confirmed,created_at,updated_at
	) VALUES (?,?,?,?, 'offered',1,0,0,?,?) ON CONFLICT(id) DO NOTHING`,
		offer.RuleID, sourceID, offer.TargetID, offer.PolicyDigest, now, now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrRuleDenied
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_rule_offers(rule_id,body) VALUES(?,?)`,
		offer.RuleID, string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListRules(ctx context.Context, deviceID string) ([]BrowserRule, error) {
	if _, err := s.deviceAllowed(ctx, deviceID); err != nil {
		return nil, ErrRuleDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.source_id,r.target_id,r.status,
		r.source_approved,r.target_approved,r.keys_confirmed,o.body
		FROM browser_sync_rules r
		INNER JOIN browser_sync_rule_offers o ON o.rule_id=r.id
		WHERE (r.source_id=? OR r.target_id=?) AND r.status!='revoked'
		ORDER BY r.created_at DESC LIMIT 50`, deviceID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BrowserRule, 0)
	for rows.Next() {
		var rule BrowserRule
		var source, target, confirmed int
		var body string
		if err := rows.Scan(&rule.ID, &rule.SourceID, &rule.TargetID, &rule.Status, &source, &target, &confirmed, &body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &rule.Offer); err != nil {
			return nil, fmt.Errorf("invalid stored offer: %w", err)
		}
		rule.SourceApproved, rule.TargetApproved, rule.KeysConfirmed = source != 0, target != 0, confirmed != 0
		out = append(out, rule)
	}
	return out, rows.Err()
}

func (s *Store) AcceptRule(ctx context.Context, targetID, ruleID string) error {
	return s.transitionRule(ctx, `UPDATE browser_sync_rules SET target_approved=1,updated_at=?
		WHERE id=? AND target_id=? AND status='offered' AND source_approved=1 AND target_approved=0`, targetID, ruleID)
}

func (s *Store) ConfirmRule(ctx context.Context, sourceID, ruleID string) error {
	// This final step is deliberately source-only. The source UI must explicitly
	// confirm the independently compared key fingerprint before activating.
	return s.transitionRule(ctx, `UPDATE browser_sync_rules SET keys_confirmed=1,
		status='active',updated_at=? WHERE id=? AND source_id=?
		AND status='offered' AND source_approved=1 AND target_approved=1 AND keys_confirmed=0`,
		sourceID, ruleID)
}

func (s *Store) RevokeRule(ctx context.Context, deviceID, ruleID string) error {
	return s.transitionRule(ctx, `UPDATE browser_sync_rules SET status='revoked',updated_at=?
		WHERE id=? AND (source_id=? OR target_id=?) AND status!='revoked'`,
		deviceID, ruleID)
}

func (s *Store) transitionRule(ctx context.Context, query, deviceID, ruleID string) error {
	if _, err := uuid.Parse(ruleID); err != nil {
		return ErrRuleDenied
	}
	if _, err := s.deviceAllowed(ctx, deviceID); err != nil {
		return ErrRuleDenied
	}
	args := []any{time.Now().UTC(), ruleID, deviceID}
	if strings.Contains(query, "OR target_id") {
		args = append(args, deviceID)
	}
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return ErrRuleDenied
	}
	return nil
}

// PeerList returns only public keys of approved browser devices belonging to
// the same active identity. A sender cannot enumerate unrelated identities.
func (s *Store) PeerList(ctx context.Context, deviceID string) ([]BrowserDevice, error) {
	me, err := s.deviceAllowed(ctx, deviceID)
	if err != nil {
		return nil, ErrRuleDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM browser_sync_devices
		WHERE identity_id=? AND state='approved' AND id!=? ORDER BY created_at DESC LIMIT 100`,
		me.IdentityID, me.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]BrowserDevice, 0, len(ids))
	for _, id := range ids {
		peer, err := s.Device(ctx, id)
		if err == nil {
			out = append(out, peer)
		}
	}
	return out, nil
}

// Expose the same authorization policy that routing will use when session
// envelopes are enabled in a later, independently tested implementation.
func (s *Store) ActiveRule(ctx context.Context, ruleID string) (ApprovedRule, error) {
	var r ApprovedRule
	var source, target, confirmed int
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT id,source_id,target_id,status,
		source_approved,target_approved,keys_confirmed FROM browser_sync_rules WHERE id=?`, ruleID).
		Scan(&r.ID, &r.SourceBrowserDeviceID, &r.TargetBrowserDeviceID, &status, &source, &target, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovedRule{}, ErrRuleDenied
	}
	if err != nil {
		return ApprovedRule{}, err
	}
	r.Active = status == "active"
	r.SourceApproved, r.TargetApproved, r.KeyFingerprintsVerified = source != 0, target != 0, confirmed != 0
	return r, nil
}
