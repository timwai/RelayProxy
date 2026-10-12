package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AdminRule exposes only policy metadata. Encrypted offers, policy digests,
// website origins, signatures and browser storage never leave this layer.
type AdminRule struct {
	ID             string    `json:"id"`
	SourceID       string    `json:"sourceId"`
	SourceName     string    `json:"sourceName"`
	TargetID       string    `json:"targetId"`
	TargetName     string    `json:"targetName"`
	IdentityID     string    `json:"identityId"`
	Status         string    `json:"status"`
	SourceApproved bool      `json:"sourceApproved"`
	TargetApproved bool      `json:"targetApproved"`
	KeysConfirmed  bool      `json:"keysConfirmed"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	SourceApprovedAt time.Time `json:"sourceApprovedAt"`
	TargetApprovedAt *time.Time `json:"targetApprovedAt,omitempty"`
	ActivatedAt      *time.Time `json:"activatedAt,omitempty"`
}

type AdminAuditEvent struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	DeviceID  string    `json:"deviceId,omitempty"`
	PeerID    string    `json:"peerId,omitempty"`
	RuleID    string    `json:"ruleId,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	ErrorCode string    `json:"errorCode,omitempty"`
	ActorID   string    `json:"actorId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type AdminDeviceStats struct {
	DeviceID   string     `json:"deviceId"`
	LastSyncAt *time.Time `json:"lastSyncAt,omitempty"`
	Applied    int        `json:"applied"`
	Failed     int        `json:"failed"`
	Conflicted int        `json:"conflicted"`
}

const browserAuditMaxRows = 5000
const browserAuditRetention = 30 * 24 * time.Hour

type browserAdminActorKey struct{}

// WithAdminActor tags a validated administrator action for audit attribution.
// Only the existing requireAuth+requireAdmin middleware can supply the actor.
func WithAdminActor(ctx context.Context, actorID string) context.Context {
	if actorID == "" || len(actorID) > 128 {
		return ctx
	}
	return context.WithValue(ctx, browserAdminActorKey{}, actorID)
}

func (s *Store) EnsureAdminSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_audit (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		type TEXT NOT NULL,
		device_id TEXT NOT NULL DEFAULT '',
		peer_id TEXT NOT NULL DEFAULT '',
		rule_id TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		actor_id TEXT NOT NULL DEFAULT '',
		error_code TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return err
	}
	// Earlier PR builds created browser_sync_audit without actor_id.
	// SQLite has no portable ADD COLUMN IF NOT EXISTS: inspect before migrating.
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(browser_sync_audit)`)
	if err != nil {
		return err
	}
	foundActor, foundErrorCode := false, false
	for rows.Next() {
		var columnID, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&columnID, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == "actor_id" {
			foundActor = true
		}
		if name == "error_code" {
			foundErrorCode = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !foundActor {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE browser_sync_audit ADD COLUMN actor_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !foundErrorCode {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE browser_sync_audit ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_browser_sync_audit_device
		ON browser_sync_audit(device_id,id)`)
	return err
}

// No arbitrary free-text log content is accepted, including from ACK frames.
// This function must be called inside the same transaction as the event.
func appendBrowserAudit(ctx context.Context, tx *sql.Tx, event, deviceID, peerID, ruleID, outcome string) error {
	return appendBrowserAuditDetail(ctx, tx, event, deviceID, peerID, ruleID, outcome, "")
}

// Error categories are fixed by the protocol; arbitrary browser exception text
// and website details must NEVER be stored or reflected in admin diagnostics.
func validBrowserSyncErrorCode(code string) bool {
	switch code {
	case "", "UNSPECIFIED", "CONFLICT", "PARTIAL_ROLLBACK", "NETWORK_UNAVAILABLE",
		"REPLAY_REJECTED", "DECRYPT_FAILED", "STORAGE_FAILURE",
		"INDEXEDDB_FAILURE", "COOKIE_FAILURE":
		return true
	}
	return false
}

func appendBrowserAuditDetail(ctx context.Context, tx *sql.Tx, event, deviceID, peerID, ruleID, outcome, errorCode string) error {
	if !validBrowserSyncErrorCode(errorCode) || (event != "delivery_result" && errorCode != "") ||
		(outcome == "APPLIED" && errorCode != "") {
		return errors.New("invalid browser audit diagnostic code")
	}
	switch event {
	case "device_approved", "device_revoked", "device_deleted",
		"rule_paused", "rule_resumed", "rule_revoked", "delivery_result", "audit_cleared":
	default:
		return errors.New("invalid browser audit event")
	}
	if event == "delivery_result" {
		switch outcome {
		case "APPLIED", "FAILED", "CONFLICT":
		default:
			return errors.New("invalid browser delivery outcome")
		}
	} else if outcome != "" {
		return errors.New("unexpected browser audit outcome")
	}
	now := time.Now().UTC()
	actorID, _ := ctx.Value(browserAdminActorKey{}).(string)
	if _, err := tx.ExecContext(ctx, `INSERT INTO browser_sync_audit
		(type,device_id,peer_id,rule_id,outcome,actor_id,error_code,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		event, deviceID, peerID, ruleID, outcome, actorID, errorCode, now); err != nil {
		return err
	}
	// Retain bounded metadata only, independent of the delivery-receipt TTL.
	if _, err := tx.ExecContext(ctx, `DELETE FROM browser_sync_audit
		WHERE created_at<? OR id <= COALESCE((SELECT MAX(id)-? FROM browser_sync_audit),-1)`,
		now.Add(-browserAuditRetention), browserAuditMaxRows); err != nil {
		return err
	}
	return nil
}

// AdminRules preserves the original metadata-only list API for callers
// that have not opted into server-side pagination.
func (s *Store) AdminRules(ctx context.Context) ([]AdminRule, error) {
	return s.adminRulesQuery(ctx, 500, 0, "", "", "")
}

// AdminRulesPage filters and pages BEFORE applying the limit. Clients must
// treat the 500-row legacy endpoint as truncated and use this for full lists.
func (s *Store) AdminRulesPage(ctx context.Context, limit, offset int, identityID, status, search string) ([]AdminRule, error) {
	if limit < 1 || limit > 100 || offset < 0 || offset > 100000 ||
		len(identityID) > 128 || len(search) > 100 {
		return nil, errors.New("invalid browser rule pagination")
	}
	if status != "" {
		switch status {
		case "offered", "active", "paused", "revoked":
		default:
			return nil, errors.New("invalid browser rule status")
		}
	}
	return s.adminRulesQuery(ctx, limit, offset, identityID, status, search)
}

func (s *Store) adminRulesQuery(ctx context.Context, limit, offset int, identityID, status, search string) ([]AdminRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.source_id,sd.name,r.target_id,td.name,
		sd.identity_id,r.status,r.source_approved,r.target_approved,r.keys_confirmed,
		r.created_at,r.updated_at,r.target_approved_at,r.activated_at FROM browser_sync_rules r
		INNER JOIN browser_sync_devices sd ON sd.id=r.source_id
		INNER JOIN browser_sync_devices td ON td.id=r.target_id
		WHERE (?='' OR sd.identity_id=?)
		  AND (?='' OR r.status=?)
		  AND (?='' OR instr(lower(r.id),lower(?))>0
		    OR instr(lower(r.source_id),lower(?))>0
		    OR instr(lower(r.target_id),lower(?))>0
		    OR instr(lower(sd.name),lower(?))>0
		    OR instr(lower(td.name),lower(?))>0)
		ORDER BY r.updated_at DESC,r.id DESC LIMIT ? OFFSET ?`,
		identityID, identityID, status, status,
		search, search, search, search, search, search,
		limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AdminRule, 0)
	for rows.Next() {
		var rule AdminRule
		var source, target, keys int
		var targetAt, activatedAt sql.NullTime
		if err := rows.Scan(&rule.ID, &rule.SourceID, &rule.SourceName, &rule.TargetID, &rule.TargetName,
			&rule.IdentityID, &rule.Status, &source, &target, &keys, &rule.CreatedAt, &rule.UpdatedAt,
			&targetAt, &activatedAt); err != nil {
			return nil, err
		}
		rule.SourceApproved, rule.TargetApproved, rule.KeysConfirmed = source != 0, target != 0, keys != 0
		rule.SourceApprovedAt = rule.CreatedAt
		if targetAt.Valid {
			rule.TargetApprovedAt = &targetAt.Time
		}
		if activatedAt.Valid {
			rule.ActivatedAt = &activatedAt.Time
		}
		items = append(items, rule)
	}
	return items, rows.Err()
}

// AdminRuleAction is an administrative freeze/unfreeze/revoke, not a browser
// consent operation. Resume requires both prior cryptographic approvals and
// currently active devices, matching active identities and capabilities.
func (s *Store) AdminRuleAction(ctx context.Context, id, action string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrRuleDenied
	}
	switch action {
	case "pause", "resume", "revoke":
	default:
		return ErrRuleDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, sourceID, targetID, sourceState, targetState string
	var sourceIdentity, targetIdentity, identityStatus string
	var send, receive, sourceOK, targetOK, confirmed, originRevoked int
	err = tx.QueryRowContext(ctx, `SELECT r.status,r.source_id,r.target_id,sd.state,td.state,
		sd.identity_id,td.identity_id,COALESCE(i.status,''),
		sd.can_send,td.can_receive,r.source_approved,r.target_approved,r.keys_confirmed,
		EXISTS (SELECT 1 FROM browser_sync_device_extensions bx
		JOIN browser_sync_extensions ex ON ex.extension_id=bx.extension_id
		WHERE bx.device_id IN (r.source_id,r.target_id) AND ex.state='revoked')
		FROM browser_sync_rules r
		JOIN browser_sync_devices sd ON sd.id=r.source_id
		JOIN browser_sync_devices td ON td.id=r.target_id
		LEFT JOIN identities i ON i.id=sd.identity_id WHERE r.id=?`, id).
		Scan(&current, &sourceID, &targetID, &sourceState, &targetState, &sourceIdentity,
			&targetIdentity, &identityStatus, &send, &receive, &sourceOK, &targetOK, &confirmed, &originRevoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	targetStateValue, event := "", ""
	switch action {
	case "pause":
		if current != "active" {
			return ErrRuleDenied
		}
		targetStateValue, event = "paused", "rule_paused"
	case "resume":
		if current != "paused" || sourceState != "approved" || targetState != "approved" ||
			sourceIdentity == "" || sourceIdentity != targetIdentity || identityStatus != "active" ||
			send != 1 || receive != 1 || sourceOK != 1 || targetOK != 1 || confirmed != 1 || originRevoked != 0 {
			return ErrRuleDenied
		}
		targetStateValue, event = "active", "rule_resumed"
	case "revoke":
		if current == "revoked" {
			return ErrRuleDenied
		}
		targetStateValue, event = "revoked", "rule_revoked"
	}
	result, err := tx.ExecContext(ctx, `UPDATE browser_sync_rules SET status=?,updated_at=?
		WHERE id=? AND status=?`, targetStateValue, time.Now().UTC(), id, current)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrRuleDenied
	}
	if err := appendBrowserAudit(ctx, tx, event, sourceID, targetID, id, ""); err != nil {
		return fmt.Errorf("browser rule audit: %w", err)
	}
	return tx.Commit()
}

// Cursor pages are bounded, ordered by internal ID and omit opaque policies.
func (s *Store) AdminAudit(ctx context.Context, before int64, limit int) ([]AdminAuditEvent, error) {
	return s.AdminAuditFiltered(ctx, before, limit, "", "")
}

// AdminAuditFiltered filters before pagination so a sparse event category
// cannot appear empty just because a page of unrelated events was fetched.
func (s *Store) AdminAuditFiltered(ctx context.Context, before int64, limit int, eventType, deviceID string) ([]AdminAuditEvent, error) {
	if before < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid browser audit pagination")
	}
	if eventType != "" {
		switch eventType {
		case "device_approved", "device_revoked", "device_deleted",
			"rule_paused", "rule_resumed", "rule_revoked", "delivery_result", "audit_cleared":
		default:
			return nil, errors.New("invalid browser audit event filter")
		}
	}
	if deviceID != "" && !deviceIDPattern.MatchString(deviceID) {
		return nil, errors.New("invalid browser audit device")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,type,device_id,peer_id,rule_id,outcome,actor_id,error_code,created_at
		FROM browser_sync_audit WHERE (?=0 OR id<?)
		AND (?='' OR type=?)
		AND (?='' OR device_id=? OR peer_id=?)
		AND created_at>=?
		ORDER BY id DESC LIMIT ?`,
		before, before, eventType, eventType, deviceID, deviceID, deviceID,
		time.Now().UTC().Add(-browserAuditRetention), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AdminAuditEvent, 0)
	for rows.Next() {
		var event AdminAuditEvent
		if err := rows.Scan(&event.ID, &event.Type, &event.DeviceID, &event.PeerID,
			&event.RuleID, &event.Outcome, &event.ActorID, &event.ErrorCode, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AdminAuditExport returns at most the retained 5000 events, using the same
// validated filters as the paged administrator view.
func (s *Store) AdminAuditExport(ctx context.Context, eventType, deviceID string) ([]AdminAuditEvent, error) {
	all := make([]AdminAuditEvent, 0)
	var before int64
	for len(all) < browserAuditMaxRows {
		limit := 100
		if remaining := browserAuditMaxRows - len(all); remaining < limit {
			limit = remaining
		}
		page, err := s.AdminAuditFiltered(ctx, before, limit, eventType, deviceID)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < limit {
			break
		}
		before = page[len(page)-1].ID
	}
	return all, nil
}

// ClearAdminAudit explicitly deletes the retained audit history, then writes
// a new administrator-attributed clearing event in the SAME transaction.
// Auditors can still see that the history was deliberately cleared.
func (s *Store) ClearAdminAudit(ctx context.Context) (int64, error) {
	actor, _ := ctx.Value(browserAdminActorKey{}).(string)
	if actor == "" {
		return 0, ErrRuleDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM browser_sync_audit`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := appendBrowserAudit(ctx, tx, "audit_cleared", "", "", "", ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) AdminDeviceStats(ctx context.Context) ([]AdminDeviceStats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id,CAST(strftime('%s',MAX(CASE WHEN type='delivery_result' AND outcome='APPLIED' THEN created_at ELSE NULL END)) AS INTEGER),
		SUM(CASE WHEN type='delivery_result' AND outcome='APPLIED' THEN 1 ELSE 0 END),
		SUM(CASE WHEN type='delivery_result' AND outcome='FAILED' THEN 1 ELSE 0 END),
		SUM(CASE WHEN type='delivery_result' AND outcome='CONFLICT' THEN 1 ELSE 0 END)
		FROM (SELECT device_id,type,outcome,created_at FROM browser_sync_audit
		UNION ALL SELECT peer_id,type,outcome,created_at FROM browser_sync_audit WHERE peer_id!='')
		WHERE device_id!='' AND created_at>=? GROUP BY device_id LIMIT 1000`,
		time.Now().UTC().Add(-browserAuditRetention))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stats := make([]AdminDeviceStats, 0)
	for rows.Next() {
		var item AdminDeviceStats
		var last sql.NullInt64
		if err := rows.Scan(&item.DeviceID, &last, &item.Applied, &item.Failed, &item.Conflicted); err != nil {
			return nil, err
		}
		if last.Valid {
			lastTime := time.Unix(last.Int64, 0).UTC()
			item.LastSyncAt = &lastTime
		}
		stats = append(stats, item)
	}
	return stats, rows.Err()
}
