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
	Capabilities   []string
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
		`CREATE TABLE IF NOT EXISTS device_identity_grants (
			id VARCHAR(64) PRIMARY KEY,
			target_device_id VARCHAR(36) NOT NULL,
			grantee_identity_id VARCHAR(64) NOT NULL,
			features TEXT NOT NULL,
			expires_at TIMESTAMP,
			revision BIGINT NOT NULL DEFAULT 1,
			created_by VARCHAR(36) NOT NULL,
			updated_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE(target_device_id, grantee_identity_id),
			FOREIGN KEY(target_device_id) REFERENCES devices(id) ON DELETE CASCADE,
			FOREIGN KEY(grantee_identity_id) REFERENCES identities(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS system_identity_grants (
			id VARCHAR(64) PRIMARY KEY,
			resource_id VARCHAR(64) NOT NULL,
			grantee_identity_id VARCHAR(64) NOT NULL,
			features TEXT NOT NULL,
			expires_at TIMESTAMP,
			revision BIGINT NOT NULL DEFAULT 1,
			created_by VARCHAR(36) NOT NULL,
			updated_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			UNIQUE(resource_id, grantee_identity_id),
			FOREIGN KEY(grantee_identity_id) REFERENCES identities(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_keys_identity ON identity_access_keys(identity_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_keys_digest ON identity_access_keys(key_digest)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_memberships_user ON identity_memberships(user_id, identity_id)`,
		`CREATE INDEX IF NOT EXISTS idx_device_identity_grants_target ON device_identity_grants(target_device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_device_identity_grants_grantee ON device_identity_grants(grantee_identity_id, target_device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_identity_grants_resource ON system_identity_grants(resource_id, grantee_identity_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_identity_grants_grantee ON system_identity_grants(grantee_identity_id, resource_id)`,
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
			"after":  map[string]any{"name": nextName, "status": nextStatus, "capabilities": nextCapabilities, "policyRevision": nextRevision},
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

		// A grant shares one concrete target device from its current identity.
		// Moving that target to another identity must not silently carry old
		// cross-identity shares into the new ownership boundary.
		rows, err := tx.Query(`SELECT id, grantee_identity_id, features
			FROM device_identity_grants WHERE target_device_id = ? ORDER BY id`, deviceID)
		if err != nil {
			return nil, err
		}
		removedGrants := make([]map[string]any, 0)
		for rows.Next() {
			var grantID, granteeIdentityID, rawFeatures string
			if err := rows.Scan(&grantID, &granteeIdentityID, &rawFeatures); err != nil {
				_ = rows.Close()
				return nil, err
			}
			features, err := decodeGrantFeatures(rawFeatures)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			removedGrants = append(removedGrants, map[string]any{
				"id": grantID, "granteeIdentityId": granteeIdentityID, "features": features,
			})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		if len(removedGrants) > 0 {
			if _, err := tx.Exec(`DELETE FROM device_identity_grants WHERE target_device_id = ?`, deviceID); err != nil {
				return nil, err
			}
			if err := insertAuthorizationAudit(tx, "device_identity_grant.reset_on_identity_move", actor, "device", deviceID,
				map[string]any{"beforeIdentityId": current.String, "afterIdentityId": identityID, "removedGrants": removedGrants}, now); err != nil {
				return nil, err
			}
		}

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

// ObserveIdentityDevice auto-enrolls a v4 device after its identity access key
// and Ed25519 proof have been validated by the gateway. Identity is derived
// exclusively from the server-side access-key record.
func (db *DB) ObserveIdentityDevice(access IdentityAccessAuthorization, observation DeviceIdentityObservation) (*DeviceAuthorization, error) {
	if access.IdentityID == "" || access.KeyID == "" {
		return nil, ErrInvalidIdentityAccessKey
	}
	if observation.Fingerprint == "" || observation.InstallationID == "" || len(observation.PublicKey) == 0 {
		return nil, errors.New("incomplete device identity")
	}
	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The challenge snapshot can outlive a key revocation or policy update.
	// Re-read both in the same transaction that enrolls/updates the device.
	var policy string
	err = tx.QueryRow(`SELECT i.name, i.capabilities, i.policy_revision
		FROM identity_access_keys k JOIN identities i ON i.id = k.identity_id
		WHERE k.id = ? AND k.identity_id = ? AND k.key_digest = ?
		  AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > ?)
		  AND i.status = ?`, access.KeyID, access.IdentityID, access.KeyDigest,
		now, IdentityStatusActive).Scan(&access.IdentityName, &policy, &access.PolicyRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidIdentityAccessKey
	}
	if err != nil {
		return nil, err
	}
	access.Capabilities, err = decodeCapabilities(policy)
	if err != nil {
		return nil, err
	}
	effective := intersectIdentityCapabilities(access.Capabilities, observation.RequestedCapabilities)
	if len(effective) == 0 {
		return nil, errors.New("identity policy does not allow any requested capability")
	}
	if err := validateCapabilityDependencies(effective); err != nil {
		return nil, err
	}
	requestedRaw, err := encodeCapabilities(observation.RequestedCapabilities)
	if err != nil {
		return nil, err
	}
	approvedRaw, err := encodeCapabilities(effective)
	if err != nil {
		return nil, err
	}

	var existingDeviceID sql.NullString
	var existingInstallation string
	var existingPublicKey []byte
	var identityState string
	err = tx.QueryRow(`SELECT device_id, installation_id, public_key, status
		FROM device_identities WHERE fingerprint = ?`, observation.Fingerprint).
		Scan(&existingDeviceID, &existingInstallation, &existingPublicKey, &identityState)
	switch {
	case err == nil:
		if existingInstallation != observation.InstallationID || !bytesEqual(existingPublicKey, observation.PublicKey) {
			return nil, errors.New("device identity metadata does not match its first observation")
		}
		switch identityState {
		case EnrollmentRevoked:
			return &DeviceAuthorization{State: EnrollmentRevoked, DeviceID: existingDeviceID.String}, nil
		case EnrollmentRejected:
			return &DeviceAuthorization{State: EnrollmentRejected, DeviceID: existingDeviceID.String}, nil
		case EnrollmentApproved:
			if !existingDeviceID.Valid || existingDeviceID.String == "" {
				return nil, errors.New("approved identity has no device")
			}
			var boundIdentity sql.NullString
			var approvalState string
			if err := tx.QueryRow(`SELECT identity_id, approval_state FROM devices WHERE id = ?`,
				existingDeviceID.String).Scan(&boundIdentity, &approvalState); err != nil {
				return nil, err
			}
			if approvalState == EnrollmentRevoked {
				return &DeviceAuthorization{State: EnrollmentRevoked, DeviceID: existingDeviceID.String}, nil
			}
			if !boundIdentity.Valid || boundIdentity.String == "" || boundIdentity.String != access.IdentityID {
				return nil, ErrDeviceIdentityConflict
			}
			if err := updateIdentityManagedDevice(tx, existingDeviceID.String, observation, requestedRaw, approvedRaw, effective, now); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return db.identityDeviceAuthorization(existingDeviceID.String, access, effective)
		case EnrollmentPending:
			// A previously pending legacy observation may become an automatic
			// v4 enrollment only because possession of a valid identity key is
			// now proven. Rejected/revoked observations are never resurrected.
		default:
			return nil, fmt.Errorf("unsupported device identity state %q", identityState)
		}
	case errors.Is(err, sql.ErrNoRows):
		// First observation is created below.
	default:
		return nil, err
	}

	deviceID := "dev_" + uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO devices
		(id, owner_user_id, identity_id, name, public_key_fingerprint, installation_id,
		 platform, arch, client_version, approval_state, requested_capabilities,
		 approved_capabilities, created_at, updated_at)
		VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		deviceID, access.IdentityID, fallbackDeviceName(observation.DeviceName), observation.Fingerprint,
		observation.InstallationID, observation.Platform, observation.Arch, observation.ClientVersion,
		EnrollmentApproved, requestedRaw, approvedRaw, now, now); err != nil {
		return nil, err
	}
	if err == nil && identityState == EnrollmentPending {
		result, err := tx.Exec(`UPDATE device_identities SET device_id = ?, status = ?, updated_at = ?
			WHERE fingerprint = ? AND status = ?`,
			deviceID, EnrollmentApproved, now, observation.Fingerprint, EnrollmentPending)
		if err != nil {
			return nil, err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return nil, err
			}
			return nil, errors.New("pending identity changed during automatic enrollment")
		}
		if _, err := tx.Exec(`UPDATE device_enrollment_requests
			SET state = ?, reviewed_at = ?, reviewed_by = ?, rejection_reason = ''
			WHERE fingerprint = ? AND state = ?`,
			EnrollmentApproved, now, "identity:"+access.IdentityID, observation.Fingerprint, EnrollmentPending); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(`INSERT INTO device_identities
			(fingerprint, device_id, installation_id, public_key, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			observation.Fingerprint, deviceID, observation.InstallationID, observation.PublicKey,
			EnrollmentApproved, now, now); err != nil {
			return nil, err
		}
	}
	if err := replaceIdentityDeviceGrants(tx, deviceID, access.IdentityID, effective, now); err != nil {
		return nil, err
	}
	if err := ensureIdentityRDPService(tx, deviceID, fallbackDeviceName(observation.DeviceName), effective, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "device.identity.auto_enroll", "identity:"+access.IdentityID, "device", deviceID,
		map[string]any{"identityId": access.IdentityID, "accessKeyId": access.KeyID, "capabilities": effective}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.identityDeviceAuthorization(deviceID, access, effective)
}

var ErrDeviceIdentityConflict = errors.New("device is already bound to another or legacy identity")

func (db *DB) identityDeviceAuthorization(deviceID string, access IdentityAccessAuthorization, effective []string) (*DeviceAuthorization, error) {
	authorization := &DeviceAuthorization{
		State: EnrollmentApproved, DeviceID: deviceID,
		IdentityID: access.IdentityID, IdentityName: access.IdentityName,
		AccessKeyID: access.KeyID, PolicyRevision: access.PolicyRevision,
		ApprovedCapabilities: append([]string(nil), effective...),
	}
	if containsCapabilityValue(effective, "rdp.controller") {
		targets, err := db.ListRDPTargetsForController(deviceID)
		if err != nil {
			return nil, err
		}
		authorization.RDPTargets = targets
	}
	return authorization, nil
}

func updateIdentityManagedDevice(tx *sql.Tx, deviceID string, observation DeviceIdentityObservation, requestedRaw, approvedRaw string, effective []string, now time.Time) error {
	if _, err := tx.Exec(`UPDATE devices SET name = ?, platform = ?, arch = ?, client_version = ?,
		requested_capabilities = ?, approved_capabilities = ?, updated_at = ?
		WHERE id = ? AND approval_state = ?`,
		fallbackDeviceName(observation.DeviceName), observation.Platform, observation.Arch,
		observation.ClientVersion, requestedRaw, approvedRaw, now, deviceID, EnrollmentApproved); err != nil {
		return err
	}
	var identityID string
	if err := tx.QueryRow(`SELECT identity_id FROM devices WHERE id = ?`, deviceID).Scan(&identityID); err != nil {
		return err
	}
	if err := replaceIdentityDeviceGrants(tx, deviceID, identityID, effective, now); err != nil {
		return err
	}
	return ensureIdentityRDPService(tx, deviceID, fallbackDeviceName(observation.DeviceName), effective, now)
}

func replaceIdentityDeviceGrants(tx *sql.Tx, deviceID, identityID string, capabilities []string, now time.Time) error {
	if _, err := tx.Exec(`DELETE FROM device_grants WHERE device_id = ?`, deviceID); err != nil {
		return err
	}
	for _, capability := range capabilities {
		if _, err := tx.Exec(`INSERT INTO device_grants (device_id, capability, granted_by, granted_at)
			VALUES (?, ?, ?, ?)`, deviceID, capability, "identity:"+identityID, now); err != nil {
			return err
		}
	}
	return nil
}

func ensureIdentityRDPService(tx *sql.Tx, deviceID, deviceName string, capabilities []string, now time.Time) error {
	if containsCapabilityValue(capabilities, "rdp.host") {
		var serviceID string
		err := tx.QueryRow(`SELECT id FROM rdp_services WHERE device_id = ?`, deviceID).Scan(&serviceID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(`INSERT INTO rdp_services
				(id, device_id, name, target_host, target_port, enabled, created_at, updated_at)
				VALUES (?, ?, ?, '127.0.0.1', 3389, TRUE, ?, ?)`,
				"rdpsvc_"+uuid.NewString(), deviceID, deviceName, now, now)
			return err
		case err != nil:
			return err
		default:
			_, err = tx.Exec(`UPDATE rdp_services SET name = ?, enabled = TRUE, updated_at = ? WHERE id = ?`,
				deviceName, now, serviceID)
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE rdp_services SET enabled = FALSE, updated_at = ? WHERE device_id = ?`, now, deviceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE rdp_port_allocations SET status = 'disabled', updated_at = ?
		WHERE service_id IN (SELECT id FROM rdp_services WHERE device_id = ?)`, now, deviceID); err != nil {
		return err
	}
	return nil
}

// IsIdentityDeviceAuthorized is the register-time recheck for a v4 session.
// It verifies the exact access key, identity, device binding and revocation
// state under the session manager authorization gate.
func (db *DB) IsIdentityDeviceAuthorized(fingerprint, deviceID, identityID, keyID string) bool {
	now := time.Now().UTC()
	var deviceCaps, identityCaps string
	err := db.QueryRow(`SELECT d.approved_capabilities, i.capabilities
		FROM device_identities di
		JOIN devices d ON d.id = di.device_id
		JOIN identities i ON i.id = d.identity_id
		JOIN identity_access_keys k ON k.identity_id = i.id
		WHERE di.fingerprint = ? AND di.device_id = ? AND di.status = ?
		  AND d.approval_state = ? AND d.identity_id = ?
		  AND i.status = ? AND k.id = ? AND k.revoked_at IS NULL
		  AND (k.expires_at IS NULL OR k.expires_at > ?)`,
		fingerprint, deviceID, EnrollmentApproved, EnrollmentApproved,
		identityID, IdentityStatusActive, keyID, now).Scan(&deviceCaps, &identityCaps)
	if err != nil {
		return false
	}
	approved, err := decodeCapabilities(deviceCaps)
	if err != nil {
		return false
	}
	allowed, err := decodeCapabilities(identityCaps)
	return err == nil && len(intersectIdentityCapabilities(allowed, approved)) == len(approved)
}

func intersectIdentityCapabilities(identityCapabilities, requested []string) []string {
	allowed := make(map[string]bool, len(identityCapabilities))
	for _, capability := range identityCapabilities {
		allowed[capability] = true
	}
	result := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, capability := range requested {
		capability = strings.TrimSpace(capability)
		if capability == "" || seen[capability] || !allowed[capability] {
			continue
		}
		seen[capability] = true
		result = append(result, capability)
	}
	return result
}

func containsCapabilityValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for index := range left {
		diff |= left[index] ^ right[index]
	}
	return diff == 0
}
