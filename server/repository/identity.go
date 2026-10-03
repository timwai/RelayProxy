package repository

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	IdentityStatusActive   = "active"
	IdentityStatusDisabled = "disabled"

	IdentityAccessKeyPrefix = "rpk_"
)

var (
	ErrInvalidIdentityAccessKey = errors.New("invalid or inactive identity access key")
	ErrIdentityRevisionConflict = errors.New("identity revision conflict")
)

type Identity struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	Capabilities   []string  `json:"capabilities"`
	PolicyRevision int64     `json:"policyRevision"`
	CreatedBy      string    `json:"createdBy"`
	UpdatedBy      string    `json:"updatedBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type IdentityUpdate struct {
	Name           *string
	Status         *string
	Capabilities   *[]string
	PolicyRevision int64
}

type IdentityAccessKey struct {
	ID         string     `json:"id"`
	IdentityID string     `json:"identityId"`
	Label      string     `json:"label,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	CreatedBy  string     `json:"createdBy"`
	RevokedBy  string     `json:"revokedBy,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type IssuedIdentityAccessKey struct {
	IdentityAccessKey
	AccessKey string `json:"accessKey"`
}

type IdentityAccessAuthorization struct {
	KeyID          string
	KeyDigest      string
	IdentityID     string
	IdentityName   string
	Capabilities  []string
	PolicyRevision int64
}

type DeviceIdentitySummary struct {
	DeviceID       string `json:"deviceId"`
	IdentityID     string `json:"identityId,omitempty"`
	IdentityName   string `json:"identityName,omitempty"`
	IdentityStatus string `json:"identityStatus,omitempty"`
}

func (db *DB) ensureIdentityAccessSchema() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS identities (
			id VARCHAR(64) PRIMARY KEY,
			name VARCHAR(100) NOT NULL UNIQUE,
			status VARCHAR(20) NOT NULL,
			capabilities TEXT NOT NULL,
			policy_revision BIGINT NOT NULL DEFAULT 1,
			created_by VARCHAR(36) NOT NULL,
			updated_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS identity_access_keys (
			id VARCHAR(64) PRIMARY KEY,
			identity_id VARCHAR(64) NOT NULL,
			label VARCHAR(100),
			key_digest VARCHAR(64) NOT NULL UNIQUE,
			expires_at TIMESTAMP,
			revoked_at TIMESTAMP,
			created_by VARCHAR(36) NOT NULL,
			revoked_by VARCHAR(36),
			created_at TIMESTAMP NOT NULL,
			last_used_at TIMESTAMP,
			FOREIGN KEY(identity_id) REFERENCES identities(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS identity_memberships (
			identity_id VARCHAR(64) NOT NULL,
			user_id VARCHAR(36) NOT NULL,
			role VARCHAR(30) NOT NULL DEFAULT 'member',
			created_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY(identity_id, user_id),
			FOREIGN KEY(identity_id) REFERENCES identities(id) ON DELETE CASCADE,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_keys_identity ON identity_access_keys(identity_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_keys_digest ON identity_access_keys(key_digest)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_memberships_user ON identity_memberships(user_id, identity_id)`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	if err := db.ensureSQLiteColumn("devices", "identity_id", "VARCHAR(64)"); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_identity ON devices(identity_id)`); err != nil {
		return err
	}
	return nil
}

func (db *DB) CreateIdentity(name, actor string, capabilities []string) (*Identity, error) {
	name = strings.TrimSpace(name)
	actor = strings.TrimSpace(actor)
	if name == "" {
		return nil, errors.New("identity name is required")
	}
	if len(name) > 100 {
		return nil, errors.New("identity name is too long")
	}
	if actor == "" {
		return nil, errors.New("actor is required")
	}
	capabilities, err := normalizeIdentityCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	raw, err := encodeCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &Identity{
		ID: "idn_" + uuid.NewString(), Name: name, Status: IdentityStatusActive,
		Capabilities: capabilities, PolicyRevision: 1,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO identities
		(id, name, status, capabilities, policy_revision, created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.Name, item.Status, raw, item.PolicyRevision, actor, actor, now, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "identity.create", actor, "identity", item.ID,
		map[string]any{"name": item.Name, "capabilities": item.Capabilities}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (db *DB) ListIdentities() ([]*Identity, error) {
	rows, err := db.Query(`SELECT id, name, status, capabilities, policy_revision,
		created_by, updated_by, created_at, updated_at
		FROM identities ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*Identity, 0)
	for rows.Next() {
		item := &Identity{}
		var raw string
		if err := rows.Scan(&item.ID, &item.Name, &item.Status, &raw, &item.PolicyRevision,
			&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if item.Capabilities, err = decodeCapabilities(raw); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) GetIdentity(id string) (*Identity, error) {
	item := &Identity{}
	var raw string
	err := db.QueryRow(`SELECT id, name, status, capabilities, policy_revision,
		created_by, updated_by, created_at, updated_at
		FROM identities WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.Name, &item.Status, &raw, &item.PolicyRevision,
		&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	item.Capabilities, err = decodeCapabilities(raw)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (db *DB) UpdateIdentity(id, actor string, update IdentityUpdate) (*Identity, error) {
	id, actor = strings.TrimSpace(id), strings.TrimSpace(actor)
	if id == "" || actor == "" {
		return nil, errors.New("identity id and actor are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	current := &Identity{}
	var raw string
	if err := tx.QueryRow(`SELECT id, name, status, capabilities, policy_revision,
		created_by, updated_by, created_at, updated_at FROM identities WHERE id = ?`, id).Scan(
		&current.ID, &current.Name, &current.Status, &raw, &current.PolicyRevision,
		&current.CreatedBy, &current.UpdatedBy, &current.CreatedAt, &current.UpdatedAt); err != nil {
		return nil, err
	}
	if current.Capabilities, err = decodeCapabilities(raw); err != nil {
		return nil, err
	}
	if update.PolicyRevision > 0 && update.PolicyRevision != current.PolicyRevision {
		return nil, ErrIdentityRevisionConflict
	}

	nextName := current.Name
	if update.Name != nil {
		nextName = strings.TrimSpace(*update.Name)
		if nextName == "" {
			return nil, errors.New("identity name is required")
		}
		if len(nextName) > 100 {
			return nil, errors.New("identity name is too long")
		}
	}
	nextStatus := current.Status
	if update.Status != nil {
		nextStatus = strings.TrimSpace(*update.Status)
		if nextStatus != IdentityStatusActive && nextStatus != IdentityStatusDisabled {
			return nil, errors.New("identity status must be active or disabled")
		}
	}
	nextCapabilities := current.Capabilities
	if update.Capabilities != nil {
		nextCapabilities, err = normalizeIdentityCapabilities(*update.Capabilities)
		if err != nil {
			return nil, err
		}
	}
	nextRaw, err := encodeCapabilities(nextCapabilities)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	nextRevision := current.PolicyRevision + 1
	result, err := tx.Exec(`UPDATE identities SET name = ?, status = ?, capabilities = ?,
		policy_revision = ?, updated_by = ?, updated_at = ?
		WHERE id = ? AND policy_revision = ?`,
		nextName, nextStatus, nextRaw, nextRevision, actor, now, id, current.PolicyRevision)
	if err != nil {
		return nil, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, ErrIdentityRevisionConflict
	}
	if err := insertAuthorizationAudit(tx, "identity.update", actor, "identity", id,
		map[string]any{
			"before": map[string]any{"name": current.Name, "status": current.Status, "capabilities": current.Capabilities, "policyRevision": current.PolicyRevision},
			"after": map[string]any{"name": nextName, "status": nextStatus, "capabilities": nextCapabilities, "policyRevision": nextRevision},
		}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	current.Name = nextName
	current.Status = nextStatus
	current.Capabilities = nextCapabilities
	current.PolicyRevision = nextRevision
	current.UpdatedBy = actor
	current.UpdatedAt = now
	return current, nil
}

func (db *DB) IssueIdentityAccessKey(identityID, actor, label string, expiresAt *time.Time) (*IssuedIdentityAccessKey, error) {
	identityID, actor, label = strings.TrimSpace(identityID), strings.TrimSpace(actor), strings.TrimSpace(label)
	if identityID == "" || actor == "" {
		return nil, errors.New("identity id and actor are required")
	}
	if len(label) > 100 {
		return nil, errors.New("access key label is too long")
	}
	if expiresAt != nil {
		value := expiresAt.UTC()
		if !value.After(time.Now().UTC()) {
			return nil, errors.New("access key expiry must be in the future")
		}
		expiresAt = &value
	}
	identity, err := db.GetIdentity(identityID)
	if err != nil {
		return nil, err
	}
	if identity.Status != IdentityStatusActive {
		return nil, errors.New("cannot issue an access key for a disabled identity")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate access key: %w", err)
	}
	plain := IdentityAccessKeyPrefix + base64.RawURLEncoding.EncodeToString(secret)
	digest := identityAccessKeyDigest(plain)
	now := time.Now().UTC()
	item := &IssuedIdentityAccessKey{
		IdentityAccessKey: IdentityAccessKey{
			ID: "iak_" + uuid.NewString(), IdentityID: identityID, Label: label,
			ExpiresAt: expiresAt, CreatedBy: actor, CreatedAt: now,
		},
		AccessKey: plain,
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO identity_access_keys
		(id, identity_id, label, key_digest, expires_at, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.ID, identityID, nullableString(label), digest, expiresAt, actor, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "identity.access_key.issue", actor, "identity_access_key", item.ID,
		map[string]any{"identityId": identityID, "label": label, "expiresAt": expiresAt}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (db *DB) ListIdentityAccessKeys(identityID string) ([]*IdentityAccessKey, error) {
	rows, err := db.Query(`SELECT id, identity_id, COALESCE(label, ''), expires_at, revoked_at,
		created_by, COALESCE(revoked_by, ''), created_at, last_used_at
		FROM identity_access_keys WHERE identity_id = ? ORDER BY created_at DESC, id DESC`, strings.TrimSpace(identityID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*IdentityAccessKey, 0)
	for rows.Next() {
		item := &IdentityAccessKey{}
		var expires, revoked, lastUsed sql.NullTime
		if err := rows.Scan(&item.ID, &item.IdentityID, &item.Label, &expires, &revoked,
			&item.CreatedBy, &item.RevokedBy, &item.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if expires.Valid {
			value := expires.Time
			item.ExpiresAt = &value
		}
		if revoked.Valid {
			value := revoked.Time
			item.RevokedAt = &value
		}
		if lastUsed.Valid {
			value := lastUsed.Time
			item.LastUsedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) RevokeIdentityAccessKey(identityID, keyID, actor string) error {
	identityID, keyID, actor = strings.TrimSpace(identityID), strings.TrimSpace(keyID), strings.TrimSpace(actor)
	if identityID == "" || keyID == "" || actor == "" {
		return errors.New("identity id, key id and actor are required")
	}
	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE identity_access_keys SET revoked_at = ?, revoked_by = ?
		WHERE id = ? AND identity_id = ? AND revoked_at IS NULL`, now, actor, keyID, identityID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM identity_access_keys WHERE id = ? AND identity_id = ?`, keyID, identityID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		return tx.Commit()
	}
	if err := insertAuthorizationAudit(tx, "identity.access_key.revoke", actor, "identity_access_key", keyID,
		map[string]any{"identityId": identityID}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ResolveIdentityAccessKey(accessKey string) (*IdentityAccessAuthorization, error) {
	accessKey = strings.TrimSpace(accessKey)
	if accessKey == "" || !strings.HasPrefix(accessKey, IdentityAccessKeyPrefix) {
		return nil, ErrInvalidIdentityAccessKey
	}
	digest := identityAccessKeyDigest(accessKey)
	now := time.Now().UTC()
	var authorization IdentityAccessAuthorization
	var raw string
	err := db.QueryRow(`SELECT k.id, k.key_digest, i.id, i.name, i.capabilities, i.policy_revision
		FROM identity_access_keys k
		JOIN identities i ON i.id = k.identity_id
		WHERE k.key_digest = ?
		  AND k.revoked_at IS NULL
		  AND (k.expires_at IS NULL OR k.expires_at > ?)
		  AND i.status = ?`, digest, now, IdentityStatusActive).Scan(
		&authorization.KeyID, &authorization.KeyDigest, &authorization.IdentityID,
		&authorization.IdentityName, &raw, &authorization.PolicyRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidIdentityAccessKey
	}
	if err != nil {
		return nil, err
	}
	authorization.Capabilities, err = decodeCapabilities(raw)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE identity_access_keys SET last_used_at = ? WHERE id = ?`, now, authorization.KeyID); err != nil {
		return nil, err
	}
	return &authorization, nil
}

func (db *DB) SetDeviceIdentity(deviceID, identityID, actor string) (*DeviceIdentitySummary, error) {
	deviceID, identityID, actor = strings.TrimSpace(deviceID), strings.TrimSpace(identityID), strings.TrimSpace(actor)
	if deviceID == "" || actor == "" {
		return nil, errors.New("device id and actor are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var current sql.NullString
	if err := tx.QueryRow(`SELECT identity_id FROM devices WHERE id = ?`, deviceID).Scan(&current); err != nil {
		return nil, err
	}
	if identityID != "" {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM identities WHERE id = ?`, identityID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists != 1 {
			return nil, sql.ErrNoRows
		}
	}
	if current.String != identityID || current.Valid != (identityID != "") {
		now := time.Now().UTC()
		if _, err := tx.Exec(`UPDATE devices SET identity_id = ?, updated_at = ? WHERE id = ?`,
			nullableString(identityID), now, deviceID); err != nil {
			return nil, err
		}
		if err := insertAuthorizationAudit(tx, "device.identity.update", actor, "device", deviceID,
			map[string]any{"before": current.String, "after": identityID}, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetDeviceIdentitySummary(deviceID)
}

func (db *DB) GetDeviceIdentitySummary(deviceID string) (*DeviceIdentitySummary, error) {
	item := &DeviceIdentitySummary{DeviceID: strings.TrimSpace(deviceID)}
	var identityID, name, status sql.NullString
	err := db.QueryRow(`SELECT d.identity_id, i.name, i.status
		FROM devices d LEFT JOIN identities i ON i.id = d.identity_id WHERE d.id = ?`, item.DeviceID).
		Scan(&identityID, &name, &status)
	if err != nil {
		return nil, err
	}
	item.IdentityID, item.IdentityName, item.IdentityStatus = identityID.String, name.String, status.String
	return item, nil
}

func (db *DB) ListDeviceIDsForIdentity(identityID string) ([]string, error) {
	rows, err := db.Query(`SELECT id FROM devices WHERE identity_id = ? ORDER BY id`, strings.TrimSpace(identityID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func identityAccessKeyDigest(accessKey string) string {
	sum := sha256.Sum256([]byte(accessKey))
	return hex.EncodeToString(sum[:])
}

func normalizeIdentityCapabilities(input []string) ([]string, error) {
	if len(input) == 0 {
		input = []string{"proxy.client"}
	}
	allowedOrder := []string{"proxy.client", "proxy.exit", "rdp.controller", "rdp.host", "rdp.public"}
	selected := make(map[string]bool, len(input))
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		known := false
		for _, candidate := range allowedOrder {
			if value == candidate {
				known = true
				break
			}
		}
		if !known {
			return nil, fmt.Errorf("unsupported identity capability %q", value)
		}
		selected[value] = true
	}
	result := make([]string, 0, len(selected))
	for _, capability := range allowedOrder {
		if selected[capability] {
			result = append(result, capability)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one identity capability is required")
	}
	if err := validateCapabilityDependencies(result); err != nil {
		return nil, err
	}
	return result, nil
}
