package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const SystemResourceServerExit = "server"

var (
	ErrSystemIdentityGrantExists   = errors.New("system identity grant already exists")
	ErrSystemIdentityGrantRevision = errors.New("system identity grant revision conflict")
)

type SystemIdentityGrant struct {
	ID                  string     `json:"id"`
	ResourceID          string     `json:"resourceId"`
	GranteeIdentityID   string     `json:"granteeIdentityId"`
	GranteeIdentityName string     `json:"granteeIdentityName"`
	Features            []string   `json:"features"`
	ExpiresAt           *time.Time `json:"expiresAt,omitempty"`
	Revision            int64      `json:"revision"`
	CreatedBy           string     `json:"createdBy"`
	UpdatedBy           string     `json:"updatedBy"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type SystemIdentityGrantUpdate struct {
	GranteeIdentityID *string
	Features          *[]string
	ExpiresAt         *time.Time
	ClearExpiresAt    bool
	Revision          int64
}

func normalizeSystemGrant(resourceID string, features []string) (string, []string, error) {
	resourceID = strings.TrimSpace(resourceID)
	if resourceID != SystemResourceServerExit {
		return "", nil, fmt.Errorf("unsupported system resource %q", resourceID)
	}
	features, err := normalizeDeviceIdentityGrantFeatures(features)
	if err != nil {
		return "", nil, err
	}
	if len(features) != 1 || features[0] != GrantFeatureProxyUse {
		return "", nil, errors.New("server exit supports only proxy.use")
	}
	return resourceID, features, nil
}

func (db *DB) validateSystemIdentityGrant(tx *sql.Tx, resourceID, granteeIdentityID string, features []string) (string, []string, error) {
	resourceID, features, err := normalizeSystemGrant(resourceID, features)
	if err != nil {
		return "", nil, err
	}
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	if granteeIdentityID == "" {
		return "", nil, errors.New("grantee identity id is required")
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM identities WHERE id = ?`, granteeIdentityID).Scan(&exists); err != nil {
		return "", nil, err
	}
	if exists != 1 {
		return "", nil, sql.ErrNoRows
	}
	return resourceID, features, nil
}

func (db *DB) CreateSystemIdentityGrant(resourceID, granteeIdentityID, actor string, features []string, expiresAt *time.Time) (*SystemIdentityGrant, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, errors.New("actor is required")
	}
	expiresAt, err := validateGrantExpiry(expiresAt)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	resourceID, features, err = db.validateSystemIdentityGrant(tx, resourceID, granteeIdentityID, features)
	if err != nil {
		return nil, err
	}
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	raw, err := encodeGrantFeatures(features)
	if err != nil {
		return nil, err
	}
	var duplicate int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM system_identity_grants
		WHERE resource_id = ? AND grantee_identity_id = ?`, resourceID, granteeIdentityID).Scan(&duplicate); err != nil {
		return nil, err
	}
	if duplicate != 0 {
		return nil, ErrSystemIdentityGrantExists
	}
	now := time.Now().UTC()
	item := &SystemIdentityGrant{
		ID: "sig_" + uuid.NewString(), ResourceID: resourceID, GranteeIdentityID: granteeIdentityID,
		Features: features, ExpiresAt: expiresAt, Revision: 1,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO system_identity_grants
		(id, resource_id, grantee_identity_id, features, expires_at, revision,
		 created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, resourceID, granteeIdentityID, raw, expiresAt, item.Revision,
		actor, actor, now, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "system_identity_grant.create", actor, "system_identity_grant", item.ID,
		map[string]any{"resourceId": resourceID, "granteeIdentityId": granteeIdentityID, "features": features, "expiresAt": expiresAt}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetSystemIdentityGrant(item.ID)
}

func (db *DB) GetSystemIdentityGrant(id string) (*SystemIdentityGrant, error) {
	item := &SystemIdentityGrant{}
	var raw string
	var expires sql.NullTime
	err := db.QueryRow(`SELECT grant_record.id, grant_record.resource_id,
		grant_record.grantee_identity_id, identity.name, grant_record.features,
		grant_record.expires_at, grant_record.revision, grant_record.created_by,
		grant_record.updated_by, grant_record.created_at, grant_record.updated_at
		FROM system_identity_grants grant_record
		JOIN identities identity ON identity.id = grant_record.grantee_identity_id
		WHERE grant_record.id = ?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.ResourceID, &item.GranteeIdentityID, &item.GranteeIdentityName,
		&raw, &expires, &item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	item.Features, err = decodeGrantFeatures(raw)
	if err != nil {
		return nil, err
	}
	if expires.Valid {
		value := expires.Time.UTC()
		item.ExpiresAt = &value
	}
	return item, nil
}

func (db *DB) ListSystemIdentityGrants(resourceID, granteeIdentityID string) ([]*SystemIdentityGrant, error) {
	resourceID = strings.TrimSpace(resourceID)
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	if resourceID != "" && resourceID != SystemResourceServerExit {
		return nil, fmt.Errorf("unsupported system resource %q", resourceID)
	}
	query := `SELECT grant_record.id, grant_record.resource_id,
		grant_record.grantee_identity_id, identity.name, grant_record.features,
		grant_record.expires_at, grant_record.revision, grant_record.created_by,
		grant_record.updated_by, grant_record.created_at, grant_record.updated_at
		FROM system_identity_grants grant_record
		JOIN identities identity ON identity.id = grant_record.grantee_identity_id
		WHERE 1 = 1`
	args := make([]any, 0, 2)
	if resourceID != "" {
		query += ` AND grant_record.resource_id = ?`
		args = append(args, resourceID)
	}
	if granteeIdentityID != "" {
		query += ` AND grant_record.grantee_identity_id = ?`
		args = append(args, granteeIdentityID)
	}
	query += ` ORDER BY identity.name, grant_record.id`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*SystemIdentityGrant, 0)
	for rows.Next() {
		item := &SystemIdentityGrant{}
		var raw string
		var expires sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.ResourceID, &item.GranteeIdentityID, &item.GranteeIdentityName,
			&raw, &expires, &item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Features, err = decodeGrantFeatures(raw)
		if err != nil {
			return nil, err
		}
		if expires.Valid {
			value := expires.Time.UTC()
			item.ExpiresAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) UpdateSystemIdentityGrant(id, actor string, update SystemIdentityGrantUpdate) (*SystemIdentityGrant, error) {
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" || actor == "" {
		return nil, errors.New("grant id and actor are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var resourceID, currentGrantee, raw string
	var currentExpires sql.NullTime
	var currentRevision int64
	if err := tx.QueryRow(`SELECT resource_id, grantee_identity_id, features, expires_at, revision
		FROM system_identity_grants WHERE id = ?`, id).
		Scan(&resourceID, &currentGrantee, &raw, &currentExpires, &currentRevision); err != nil {
		return nil, err
	}
	if update.Revision > 0 && update.Revision != currentRevision {
		return nil, ErrSystemIdentityGrantRevision
	}
	currentFeatures, err := decodeGrantFeatures(raw)
	if err != nil {
		return nil, err
	}
	nextGrantee := currentGrantee
	if update.GranteeIdentityID != nil {
		nextGrantee = strings.TrimSpace(*update.GranteeIdentityID)
	}
	nextFeatures := currentFeatures
	if update.Features != nil {
		nextFeatures = append([]string(nil), (*update.Features)...)
	}
	resourceID, nextFeatures, err = db.validateSystemIdentityGrant(tx, resourceID, nextGrantee, nextFeatures)
	if err != nil {
		return nil, err
	}
	nextRaw, err := encodeGrantFeatures(nextFeatures)
	if err != nil {
		return nil, err
	}

	var nextExpires *time.Time
	if !update.ClearExpiresAt && currentExpires.Valid {
		value := currentExpires.Time.UTC()
		nextExpires = &value
	}
	if update.ClearExpiresAt {
		nextExpires = nil
	} else if update.ExpiresAt != nil {
		nextExpires, err = validateGrantExpiry(update.ExpiresAt)
		if err != nil {
			return nil, err
		}
	}

	if nextGrantee != currentGrantee {
		var duplicate int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM system_identity_grants
			WHERE resource_id = ? AND grantee_identity_id = ? AND id <> ?`,
			resourceID, nextGrantee, id).Scan(&duplicate); err != nil {
			return nil, err
		}
		if duplicate != 0 {
			return nil, ErrSystemIdentityGrantExists
		}
	}

	now := time.Now().UTC()
	nextRevision := currentRevision + 1
	result, err := tx.Exec(`UPDATE system_identity_grants
		SET grantee_identity_id = ?, features = ?, expires_at = ?, revision = ?,
		    updated_by = ?, updated_at = ?
		WHERE id = ? AND revision = ?`,
		nextGrantee, nextRaw, nextExpires, nextRevision, actor, now, id, currentRevision)
	if err != nil {
		return nil, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rowsAffected != 1 {
		return nil, ErrSystemIdentityGrantRevision
	}
	if err := insertAuthorizationAudit(tx, "system_identity_grant.update", actor, "system_identity_grant", id,
		map[string]any{
			"before": map[string]any{"granteeIdentityId": currentGrantee, "features": currentFeatures, "revision": currentRevision},
			"after":  map[string]any{"granteeIdentityId": nextGrantee, "features": nextFeatures, "expiresAt": nextExpires, "revision": nextRevision},
		}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetSystemIdentityGrant(id)
}

func (db *DB) DeleteSystemIdentityGrant(id, actor string, revision int64) (bool, error) {
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" || actor == "" {
		return false, errors.New("grant id and actor are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var resourceID, granteeIdentityID, raw string
	var currentRevision int64
	err = tx.QueryRow(`SELECT resource_id, grantee_identity_id, features, revision
		FROM system_identity_grants WHERE id = ?`, id).
		Scan(&resourceID, &granteeIdentityID, &raw, &currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if revision > 0 && revision != currentRevision {
		return false, ErrSystemIdentityGrantRevision
	}
	features, err := decodeGrantFeatures(raw)
	if err != nil {
		return false, err
	}
	result, err := tx.Exec(`DELETE FROM system_identity_grants WHERE id = ? AND revision = ?`, id, currentRevision)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, ErrSystemIdentityGrantRevision
	}
	now := time.Now().UTC()
	if err := insertAuthorizationAudit(tx, "system_identity_grant.delete", actor, "system_identity_grant", id,
		map[string]any{"resourceId": resourceID, "granteeIdentityId": granteeIdentityID, "features": features, "revision": currentRevision}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) HasActiveSystemIdentityGrant(resourceID, granteeIdentityID, feature string) (bool, error) {
	resourceID = strings.TrimSpace(resourceID)
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	if resourceID == "" || granteeIdentityID == "" {
		return false, nil
	}
	if _, _, err := normalizeSystemGrant(resourceID, []string{feature}); err != nil {
		return false, err
	}
	rows, err := db.Query(`SELECT features FROM system_identity_grants
		WHERE resource_id = ? AND grantee_identity_id = ?
		  AND (expires_at IS NULL OR expires_at > ?)`,
		resourceID, granteeIdentityID, time.Now().UTC())
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		features, err := decodeGrantFeatures(raw)
		if err != nil {
			return false, err
		}
		if containsGrantFeature(features, feature) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// AuthorizeServerExit requires an explicit resource -> identity grant for
// identity sessions. The owner fallback is retained only for old data
// migration tooling and compatibility tests; production rejects v3 sessions.
func (db *DB) AuthorizeServerExit(clientDeviceID string) (bool, error) {
	clientDeviceID = strings.TrimSpace(clientDeviceID)
	if clientDeviceID == "" {
		return false, nil
	}
	var identityID, identityStatus sql.NullString
	var clientCaps string
	err := db.QueryRow(`SELECT device.identity_id, device.approved_capabilities, identity.status
		FROM devices device
		LEFT JOIN identities identity ON identity.id = device.identity_id
		WHERE device.id = ? AND device.approval_state = 'approved'`, clientDeviceID).
		Scan(&identityID, &clientCaps, &identityStatus)
	if err != nil {
		return false, err
	}
	if !hasCapabilityJSON(clientCaps, "proxy.client") {
		return false, nil
	}
	if !identityID.Valid || strings.TrimSpace(identityID.String) == "" {
		return true, nil
	}
	if identityStatus.String != IdentityStatusActive {
		return false, nil
	}
	return db.HasActiveSystemIdentityGrant(SystemResourceServerExit, identityID.String, GrantFeatureProxyUse)
}
