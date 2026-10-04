package repository

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	IdentityStatusActive   = "active"
	IdentityStatusDisabled = "disabled"
	IdentityPublicIDLength = 16
	identityIDAlphabet     = "abcdefghijklmnopqrstuvwxyz0123456789"
)

var (
	ErrInvalidIdentity          = errors.New("invalid or inactive identity")
	ErrIdentityRevisionConflict = errors.New("identity revision conflict")
)

type Identity struct {
	ID              string    `json:"id"`
	ShortID         string    `json:"shortId"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	PolicyRevision  int64     `json:"policyRevision"`
	LoginConfigured bool      `json:"loginConfigured"`
	LoginUsername   string    `json:"loginUsername,omitempty"`
	CreatedBy       string    `json:"createdBy"`
	UpdatedBy       string    `json:"updatedBy"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func validateIdentityShortID(value string) error {
	if len(value) != IdentityPublicIDLength {
		return errors.New("identity id must contain exactly 16 lowercase letters and digits")
	}
	hasLetter, hasDigit := false, false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			hasLetter = true
		case char >= '0' && char <= '9':
			hasDigit = true
		default:
			return errors.New("identity id must contain exactly 16 lowercase letters and digits")
		}
	}
	if hasLetter && hasDigit {
		return nil
	}
	return errors.New("identity id must contain exactly 16 lowercase letters and digits")
}

func newIdentityShortID() (string, error) {
	for {
		result := make([]byte, IdentityPublicIDLength)
		random := make([]byte, IdentityPublicIDLength*2)
		for index := 0; index < len(result); {
			if _, err := rand.Read(random); err != nil {
				return "", err
			}
			for _, value := range random {
				// 252 is the largest multiple of 36 that fits in one byte. Dropping
				// higher values avoids modulo bias without weakening the identifier.
				if value >= 252 {
					continue
				}
				result[index] = identityIDAlphabet[int(value)%len(identityIDAlphabet)]
				index++
				if index == len(result) {
					break
				}
			}
		}
		if validateIdentityShortID(string(result)) == nil {
			return string(result), nil
		}
	}
}

func normalizeIdentityUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 64 {
		return "", errors.New("login username must contain 3 to 64 letters, digits, dots, underscores or hyphens")
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return "", errors.New("login username must contain 3 to 64 letters, digits, dots, underscores or hyphens")
	}
	return value, nil
}

func (db *DB) ensureIdentityShortIDs() error {
	rows, err := db.Query(`SELECT id, COALESCE(short_id, '') FROM identities ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id, shortID string
		if err := rows.Scan(&id, &shortID); err != nil {
			_ = rows.Close()
			return err
		}
		if validateIdentityShortID(shortID) != nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		updated := false
		for attempt := 0; attempt < 16; attempt++ {
			shortID, err := newIdentityShortID()
			if err != nil {
				return err
			}
			var exists int
			if err := db.QueryRow(`SELECT COUNT(*) FROM identities WHERE short_id = ?`, shortID).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				continue
			}
			if _, err := db.Exec(`UPDATE identities SET short_id = ? WHERE id = ?`, shortID, id); err != nil {
				return err
			}
			updated = true
			break
		}
		if !updated {
			return errors.New("failed to generate a unique identity id")
		}
	}
	return nil
}

type IdentityUpdate struct {
	Name           *string
	Status         *string
	PolicyRevision int64
}

type IdentityAuthorization struct {
	IdentityID     string
	IdentityName   string
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
			short_id VARCHAR(32),
			name VARCHAR(100) NOT NULL UNIQUE,
			status VARCHAR(20) NOT NULL,
			capabilities TEXT NOT NULL,
			policy_revision BIGINT NOT NULL DEFAULT 1,
			created_by VARCHAR(36) NOT NULL,
			updated_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
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
	if err := db.ensureSQLiteColumn("identities", "short_id", "VARCHAR(32)"); err != nil {
		return err
	}
	if err := db.ensureSQLiteColumn("device_enrollment_requests", "identity_id", "VARCHAR(64)"); err != nil {
		return err
	}
	if err := db.ensureIdentityShortIDs(); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_identities_short_id ON identities(short_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_devices_identity ON devices(identity_id)`); err != nil {
		return err
	}
	return nil
}

func (db *DB) CreateIdentity(name, actor string) (*Identity, error) {
	shortID, err := newIdentityShortID()
	if err != nil {
		return nil, err
	}
	return db.createIdentityWithShortID(shortID, name, actor)
}

func (db *DB) createIdentityWithShortID(shortID, name, actor string) (*Identity, error) {
	shortID = strings.ToLower(strings.TrimSpace(shortID))
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
	if err := validateIdentityShortID(shortID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &Identity{
		ID: "idn_" + uuid.NewString(), ShortID: shortID, Name: name, Status: IdentityStatusActive,
		PolicyRevision: 1,
		CreatedBy:      actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO identities
		(id, short_id, name, status, capabilities, policy_revision, created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.ShortID, item.Name, item.Status, "[]", item.PolicyRevision, actor, actor, now, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "identity.create", actor, "identity", item.ID,
		map[string]any{"shortId": item.ShortID, "name": item.Name}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// CreateIdentityWithLogin creates an automatically identified isolation
// boundary and its independently named management account atomically.
func (db *DB) CreateIdentityWithLogin(username, name, actor, passwordHash string) (*Identity, error) {
	name, actor, passwordHash = strings.TrimSpace(name), strings.TrimSpace(actor), strings.TrimSpace(passwordHash)
	if name == "" || actor == "" || passwordHash == "" {
		return nil, errors.New("identity name, actor and password hash are required")
	}
	if len(name) > 100 {
		return nil, errors.New("identity name is too long")
	}
	username, err := normalizeIdentityUsername(username)
	if err != nil {
		return nil, err
	}
	shortID, err := newIdentityShortID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	item := &Identity{
		ID: "idn_" + uuid.NewString(), ShortID: shortID, Name: name, Status: IdentityStatusActive,
		PolicyRevision: 1, LoginConfigured: true, LoginUsername: username,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	userID := uuid.NewString()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO identities
		(id, short_id, name, status, capabilities, policy_revision, created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, '[]', ?, ?, ?, ?, ?)`, item.ID, item.ShortID, item.Name, item.Status,
		item.PolicyRevision, actor, actor, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO users
		(id, username, password_hash, display_name, role, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'user', 'active', ?, ?)`, userID, username, passwordHash, name, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO identity_memberships (identity_id, user_id, role, created_by, created_at)
		VALUES (?, ?, 'owner', ?, ?)`, item.ID, userID, actor, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "identity.create", actor, "identity", item.ID,
		map[string]any{"shortId": item.ShortID, "name": item.Name, "loginUsername": username, "loginConfigured": true}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (db *DB) ListIdentities() ([]*Identity, error) {
	rows, err := db.Query(`SELECT id, short_id, name, status, policy_revision,
		EXISTS(SELECT 1 FROM identity_memberships WHERE identity_id = identities.id),
		COALESCE((SELECT user.username FROM identity_memberships membership JOIN users user ON user.id = membership.user_id
			WHERE membership.identity_id = identities.id ORDER BY CASE membership.role WHEN 'owner' THEN 0 ELSE 1 END, membership.created_at LIMIT 1), ''),
		created_by, updated_by, created_at, updated_at
		FROM identities ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*Identity, 0)
	for rows.Next() {
		item := &Identity{}
		if err := rows.Scan(&item.ID, &item.ShortID, &item.Name, &item.Status, &item.PolicyRevision, &item.LoginConfigured, &item.LoginUsername,
			&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) GetIdentity(id string) (*Identity, error) {
	item := &Identity{}
	err := db.QueryRow(`SELECT id, short_id, name, status, policy_revision,
		EXISTS(SELECT 1 FROM identity_memberships WHERE identity_id = identities.id),
		COALESCE((SELECT user.username FROM identity_memberships membership JOIN users user ON user.id = membership.user_id
			WHERE membership.identity_id = identities.id ORDER BY CASE membership.role WHEN 'owner' THEN 0 ELSE 1 END, membership.created_at LIMIT 1), ''),
		created_by, updated_by, created_at, updated_at
		FROM identities WHERE id = ?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.ShortID, &item.Name, &item.Status, &item.PolicyRevision, &item.LoginConfigured, &item.LoginUsername,
		&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	return item, err
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
	if err := tx.QueryRow(`SELECT id, short_id, name, status, policy_revision,
		EXISTS(SELECT 1 FROM identity_memberships WHERE identity_id = identities.id),
		COALESCE((SELECT user.username FROM identity_memberships membership JOIN users user ON user.id = membership.user_id
			WHERE membership.identity_id = identities.id ORDER BY CASE membership.role WHEN 'owner' THEN 0 ELSE 1 END, membership.created_at LIMIT 1), ''),
		created_by, updated_by, created_at, updated_at FROM identities WHERE id = ?`, id).Scan(
		&current.ID, &current.ShortID, &current.Name, &current.Status, &current.PolicyRevision, &current.LoginConfigured, &current.LoginUsername,
		&current.CreatedBy, &current.UpdatedBy, &current.CreatedAt, &current.UpdatedAt); err != nil {
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
	now := time.Now().UTC()
	nextRevision := current.PolicyRevision + 1
	result, err := tx.Exec(`UPDATE identities SET name = ?, status = ?,
		policy_revision = ?, updated_by = ?, updated_at = ?
		WHERE id = ? AND policy_revision = ?`,
		nextName, nextStatus, nextRevision, actor, now, id, current.PolicyRevision)
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
	if _, err := tx.Exec(`UPDATE users SET display_name = ?, status = ?, updated_at = ?
		WHERE id IN (SELECT user_id FROM identity_memberships WHERE identity_id = ?)`,
		nextName, nextStatus, now, id); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "identity.update", actor, "identity", id,
		map[string]any{
			"before": map[string]any{"name": current.Name, "status": current.Status, "policyRevision": current.PolicyRevision},
			"after":  map[string]any{"name": nextName, "status": nextStatus, "policyRevision": nextRevision},
		}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	current.Name = nextName
	current.Status = nextStatus
	current.PolicyRevision = nextRevision
	current.UpdatedBy = actor
	current.UpdatedAt = now
	return current, nil
}

func (db *DB) ResolveIdentity(shortID string) (*IdentityAuthorization, error) {
	shortID = strings.ToLower(strings.TrimSpace(shortID))
	if err := validateIdentityShortID(shortID); err != nil {
		return nil, ErrInvalidIdentity
	}
	var authorization IdentityAuthorization
	err := db.QueryRow(`SELECT id, name, policy_revision FROM identities WHERE short_id = ? AND status = ?`,
		shortID, IdentityStatusActive).Scan(&authorization.IdentityID, &authorization.IdentityName, &authorization.PolicyRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidIdentity
	}
	return &authorization, err
}

func (db *DB) ConfigureIdentityLogin(identityID, username, passwordHash, actor string) error {
	identityID, passwordHash, actor = strings.TrimSpace(identityID), strings.TrimSpace(passwordHash), strings.TrimSpace(actor)
	if identityID == "" || passwordHash == "" || actor == "" {
		return errors.New("identity id, login username, password hash and actor are required")
	}
	username, err := normalizeIdentityUsername(username)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, status string
	if err := tx.QueryRow(`SELECT name, status FROM identities WHERE id = ?`, identityID).Scan(&name, &status); err != nil {
		return err
	}
	now := time.Now().UTC()
	var userID string
	err = tx.QueryRow(`SELECT user_id FROM identity_memberships WHERE identity_id = ? ORDER BY CASE role WHEN 'owner' THEN 0 ELSE 1 END, created_at LIMIT 1`, identityID).Scan(&userID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		userID = uuid.NewString()
		if _, err := tx.Exec(`INSERT INTO users (id, username, password_hash, display_name, role, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'user', ?, ?, ?)`, userID, username, passwordHash, name, status, now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO identity_memberships (identity_id, user_id, role, created_by, created_at)
			VALUES (?, ?, 'owner', ?, ?)`, identityID, userID, actor, now); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.Exec(`UPDATE users SET username = ?, password_hash = ?, display_name = ?, status = ?, updated_at = ? WHERE id = ?`,
			username, passwordHash, name, status, now, userID); err != nil {
			return err
		}
	}
	if err := insertAuthorizationAudit(tx, "identity.login.configure", actor, "identity", identityID,
		map[string]any{"username": username}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) GetIdentityLoginUserID(identityID string) (string, error) {
	var userID string
	err := db.QueryRow(`SELECT user_id FROM identity_memberships WHERE identity_id = ?
		ORDER BY CASE role WHEN 'owner' THEN 0 ELSE 1 END, created_at LIMIT 1`, strings.TrimSpace(identityID)).Scan(&userID)
	return userID, err
}

func (db *DB) SetDeviceIdentity(deviceID, identityID, actor string) (*DeviceIdentitySummary, error) {
	deviceID, identityID, actor = strings.TrimSpace(deviceID), strings.TrimSpace(identityID), strings.TrimSpace(actor)
	if deviceID == "" || identityID == "" || actor == "" {
		return nil, errors.New("device id, identity id and actor are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var current, currentOwner sql.NullString
	if err := tx.QueryRow(`SELECT identity_id, owner_user_id FROM devices WHERE id = ?`, deviceID).Scan(&current, &currentOwner); err != nil {
		return nil, err
	}
	var identityExists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM identities WHERE id = ?`, identityID).Scan(&identityExists); err != nil {
		return nil, err
	}
	if identityExists != 1 {
		return nil, sql.ErrNoRows
	}
	var identityOwner string
	err = tx.QueryRow(`SELECT user_id FROM identity_memberships WHERE identity_id = ?
		ORDER BY CASE role WHEN 'owner' THEN 0 ELSE 1 END, created_at LIMIT 1`, identityID).Scan(&identityOwner)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	identityChanged := current.String != identityID || !current.Valid
	ownerChanged := identityOwner != "" && currentOwner.String != identityOwner
	if identityChanged {
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

		if err := insertAuthorizationAudit(tx, "device.identity.update", actor, "device", deviceID,
			map[string]any{"before": current.String, "after": identityID}, now); err != nil {
			return nil, err
		}
	}
	if identityChanged || ownerChanged {
		// Historical owner_user_id values identify the old approving account,
		// not the isolation identity. Synchronize ownership even when the device
		// already carries this identity, which repairs assignments made before
		// independent identity logins were introduced.
		if _, err := tx.Exec(`UPDATE devices SET identity_id = ?,
			owner_user_id = CASE WHEN ? <> '' THEN ? ELSE owner_user_id END,
			updated_at = ? WHERE id = ?`, identityID, identityOwner, identityOwner, time.Now().UTC(), deviceID); err != nil {
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

// ObserveIdentityDevice records a device under the resolved identity. New devices
// remain pending until an administrator of that identity approves their capabilities.
func (db *DB) ObserveIdentityDevice(access IdentityAuthorization, observation DeviceIdentityObservation) (*DeviceAuthorization, error) {
	if access.IdentityID == "" {
		return nil, ErrInvalidIdentity
	}
	if observation.Fingerprint == "" || observation.InstallationID == "" || len(observation.PublicKey) == 0 {
		return nil, errors.New("incomplete device identity")
	}
	requested := filterApprovedCapabilities(observation.RequestedCapabilities, observation.RequestedCapabilities)
	if len(requested) == 0 {
		return nil, errors.New("device must request at least one supported capability")
	}
	if err := validateCapabilityDependencies(requested); err != nil {
		return nil, err
	}
	requestedRaw, err := encodeCapabilities(requested)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := tx.QueryRow(`SELECT name, policy_revision FROM identities WHERE id = ? AND status = ?`,
		access.IdentityID, IdentityStatusActive).Scan(&access.IdentityName, &access.PolicyRevision); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidIdentity
	} else if err != nil {
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
		case EnrollmentApproved:
			if !existingDeviceID.Valid || existingDeviceID.String == "" {
				return nil, errors.New("approved identity has no device")
			}
			var boundIdentity sql.NullString
			var approvalState, approvedRaw string
			if err := tx.QueryRow(`SELECT identity_id, approval_state, approved_capabilities FROM devices WHERE id = ?`,
				existingDeviceID.String).Scan(&boundIdentity, &approvalState, &approvedRaw); err != nil {
				return nil, err
			}
			if approvalState == EnrollmentRevoked {
				return &DeviceAuthorization{State: EnrollmentRevoked, DeviceID: existingDeviceID.String}, nil
			}
			if !boundIdentity.Valid || boundIdentity.String != access.IdentityID {
				return nil, ErrDeviceIdentityConflict
			}
			approved, err := decodeCapabilities(approvedRaw)
			if err != nil {
				return nil, err
			}
			effective := filterApprovedCapabilities(approved, requested)
			if len(effective) == 0 {
				return nil, errors.New("device does not request any approved capability")
			}
			if err := updateIdentityManagedDevice(tx, existingDeviceID.String, observation, requestedRaw, effective, now); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return db.identityDeviceAuthorization(existingDeviceID.String, access, effective)
		case EnrollmentRejected, EnrollmentRevoked:
			return &DeviceAuthorization{State: identityState, DeviceID: existingDeviceID.String}, nil
		case EnrollmentPending:
			var pendingIdentity string
			if err := tx.QueryRow(`SELECT identity_id FROM device_enrollment_requests WHERE fingerprint = ?`, observation.Fingerprint).Scan(&pendingIdentity); err != nil {
				return nil, err
			}
			if pendingIdentity != access.IdentityID {
				return nil, ErrDeviceIdentityConflict
			}
		default:
			return nil, fmt.Errorf("unsupported device identity state %q", identityState)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO device_identities
			(fingerprint, device_id, installation_id, public_key, status, created_at, updated_at)
			VALUES (?, NULL, ?, ?, ?, ?, ?)`, observation.Fingerprint, observation.InstallationID,
			observation.PublicKey, EnrollmentPending, now, now); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	requestID := "enr_" + uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO device_enrollment_requests
		(id, identity_id, fingerprint, installation_id, public_key, device_name, platform, arch, client_version,
		 requested_capabilities, state, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			device_name = excluded.device_name, platform = excluded.platform, arch = excluded.arch,
			client_version = excluded.client_version, requested_capabilities = excluded.requested_capabilities,
			last_seen_at = excluded.last_seen_at`,
		requestID, access.IdentityID, observation.Fingerprint, observation.InstallationID, observation.PublicKey,
		fallbackDeviceName(observation.DeviceName), observation.Platform, observation.Arch, observation.ClientVersion,
		requestedRaw, EnrollmentPending, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE device_identities SET updated_at = ? WHERE fingerprint = ?`, now, observation.Fingerprint); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT id FROM device_enrollment_requests WHERE fingerprint = ?`, observation.Fingerprint).Scan(&requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &DeviceAuthorization{State: EnrollmentPending, RequestID: requestID, IdentityID: access.IdentityID, IdentityName: access.IdentityName}, nil
}

var ErrDeviceIdentityConflict = errors.New("device is already bound to another identity")

func (db *DB) identityDeviceAuthorization(deviceID string, access IdentityAuthorization, effective []string) (*DeviceAuthorization, error) {
	authorization := &DeviceAuthorization{
		State: EnrollmentApproved, DeviceID: deviceID,
		IdentityID: access.IdentityID, IdentityName: access.IdentityName,
		PolicyRevision:       access.PolicyRevision,
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

func updateIdentityManagedDevice(tx *sql.Tx, deviceID string, observation DeviceIdentityObservation, requestedRaw string, effective []string, now time.Time) error {
	if _, err := tx.Exec(`UPDATE devices SET name = ?, platform = ?, arch = ?, client_version = ?,
		requested_capabilities = ?, updated_at = ?
		WHERE id = ? AND approval_state = ?`,
		fallbackDeviceName(observation.DeviceName), observation.Platform, observation.Arch,
		observation.ClientVersion, requestedRaw, now, deviceID, EnrollmentApproved); err != nil {
		return err
	}
	if err := replaceDeviceRuntimeGrants(tx, deviceID, effective, now); err != nil {
		return err
	}
	return syncDeviceRDPService(tx, deviceID, fallbackDeviceName(observation.DeviceName), effective, now)
}

func replaceDeviceRuntimeGrants(tx *sql.Tx, deviceID string, capabilities []string, now time.Time) error {
	if _, err := tx.Exec(`DELETE FROM device_grants WHERE device_id = ?`, deviceID); err != nil {
		return err
	}
	for _, capability := range capabilities {
		if _, err := tx.Exec(`INSERT INTO device_grants (device_id, capability, granted_by, granted_at)
			VALUES (?, ?, ?, ?)`, deviceID, capability, "device-auto", now); err != nil {
			return err
		}
	}
	return nil
}

func syncDeviceRDPService(tx *sql.Tx, deviceID, deviceName string, capabilities []string, now time.Time) error {
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

// IsIdentityDeviceAuthorized rechecks the approved device-to-identity binding
// immediately before a session is registered.
func (db *DB) IsIdentityDeviceAuthorized(fingerprint, deviceID, identityID string) bool {
	var count int
	err := db.QueryRow(`SELECT COUNT(*)
		FROM device_identities di
		JOIN devices d ON d.id = di.device_id
		JOIN identities i ON i.id = d.identity_id
		WHERE di.fingerprint = ? AND di.device_id = ? AND di.status = ?
		  AND d.approval_state = ? AND d.identity_id = ?
		  AND i.status = ?`,
		fingerprint, deviceID, EnrollmentApproved, EnrollmentApproved,
		identityID, IdentityStatusActive).Scan(&count)
	return err == nil && count == 1
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
