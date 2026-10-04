package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	GrantFeatureProxyUse   = "proxy.use"
	GrantFeatureRDPConnect = "rdp.connect"
)

var (
	ErrDeviceIdentityGrantExists   = errors.New("device identity grant already exists")
	ErrDeviceIdentityGrantRevision = errors.New("device identity grant revision conflict")
)

type DeviceIdentityGrant struct {
	ID                  string     `json:"id"`
	TargetDeviceID      string     `json:"targetDeviceId"`
	TargetDeviceName    string     `json:"targetDeviceName"`
	TargetIdentityID    string     `json:"targetIdentityId"`
	TargetIdentityName  string     `json:"targetIdentityName"`
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

type DeviceIdentityGrantUpdate struct {
	GranteeIdentityID *string
	Features          *[]string
	ExpiresAt         *time.Time
	ClearExpiresAt    bool
	Revision          int64
}

func normalizeDeviceIdentityGrantFeatures(features []string) ([]string, error) {
	seen := make(map[string]bool, len(features))
	out := make([]string, 0, len(features))
	for _, feature := range features {
		feature = strings.TrimSpace(feature)
		switch feature {
		case GrantFeatureProxyUse, GrantFeatureRDPConnect:
		default:
			return nil, fmt.Errorf("unsupported grant feature %q", feature)
		}
		if !seen[feature] {
			seen[feature] = true
			out = append(out, feature)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("at least one grant feature is required")
	}
	ordered := make([]string, 0, len(out))
	for _, feature := range []string{GrantFeatureProxyUse, GrantFeatureRDPConnect} {
		if seen[feature] {
			ordered = append(ordered, feature)
		}
	}
	return ordered, nil
}

func encodeGrantFeatures(features []string) (string, error) {
	normalized, err := normalizeDeviceIdentityGrantFeatures(features)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	return string(data), err
}

func decodeGrantFeatures(raw string) ([]string, error) {
	var features []string
	if err := json.Unmarshal([]byte(raw), &features); err != nil {
		return nil, fmt.Errorf("decode grant features: %w", err)
	}
	return normalizeDeviceIdentityGrantFeatures(features)
}

func validateGrantExpiry(expiresAt *time.Time) (*time.Time, error) {
	if expiresAt == nil {
		return nil, nil
	}
	value := expiresAt.UTC()
	if !value.After(time.Now().UTC()) {
		return nil, errors.New("grant expiry must be in the future")
	}
	return &value, nil
}

func (db *DB) validateDeviceIdentityGrant(tx *sql.Tx, targetDeviceID, granteeIdentityID string, features []string) ([]string, error) {
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	if targetDeviceID == "" || granteeIdentityID == "" {
		return nil, errors.New("target device id and grantee identity id are required")
	}
	features, err := normalizeDeviceIdentityGrantFeatures(features)
	if err != nil {
		return nil, err
	}

	var targetIdentity sql.NullString
	var targetCapabilities string
	if err := tx.QueryRow(`SELECT identity_id, approved_capabilities
		FROM devices WHERE id = ? AND approval_state = 'approved'`, targetDeviceID).
		Scan(&targetIdentity, &targetCapabilities); err != nil {
		return nil, err
	}
	if !targetIdentity.Valid || strings.TrimSpace(targetIdentity.String) == "" {
		return nil, errors.New("target device must be assigned to an identity")
	}
	if targetIdentity.String == granteeIdentityID {
		return nil, errors.New("same-identity access is automatic and does not require an explicit grant")
	}

	var granteeStatus string
	if err := tx.QueryRow(`SELECT status FROM identities WHERE id = ?`, granteeIdentityID).Scan(&granteeStatus); err != nil {
		return nil, err
	}
	var targetIdentityStatus string
	if err := tx.QueryRow(`SELECT status FROM identities WHERE id = ?`, targetIdentity.String).Scan(&targetIdentityStatus); err != nil {
		return nil, err
	}
	_ = granteeStatus
	_ = targetIdentityStatus

	for _, feature := range features {
		switch feature {
		case GrantFeatureProxyUse:
			if !hasCapabilityJSON(targetCapabilities, "proxy.exit") {
				return nil, errors.New("target device is not approved as a proxy exit")
			}
		case GrantFeatureRDPConnect:
			if !hasCapabilityJSON(targetCapabilities, "rdp.host") {
				return nil, errors.New("target device is not approved as an RDP host")
			}
		}
	}
	return features, nil
}

func (db *DB) CreateDeviceIdentityGrant(targetDeviceID, granteeIdentityID, actor string, features []string, expiresAt *time.Time) (*DeviceIdentityGrant, error) {
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
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
	features, err = db.validateDeviceIdentityGrant(tx, targetDeviceID, granteeIdentityID, features)
	if err != nil {
		return nil, err
	}
	raw, err := encodeGrantFeatures(features)
	if err != nil {
		return nil, err
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM device_identity_grants
		WHERE target_device_id = ? AND grantee_identity_id = ?`, targetDeviceID, granteeIdentityID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		return nil, ErrDeviceIdentityGrantExists
	}

	now := time.Now().UTC()
	item := &DeviceIdentityGrant{
		ID: "dig_" + uuid.NewString(), TargetDeviceID: targetDeviceID,
		GranteeIdentityID: granteeIdentityID, Features: features, ExpiresAt: expiresAt,
		Revision: 1, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Exec(`INSERT INTO device_identity_grants
		(id, target_device_id, grantee_identity_id, features, expires_at, revision,
		 created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, targetDeviceID, granteeIdentityID, raw, expiresAt, item.Revision,
		actor, actor, now, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "device_identity_grant.create", actor, "device_identity_grant", item.ID,
		map[string]any{"targetDeviceId": targetDeviceID, "granteeIdentityId": granteeIdentityID, "features": features, "expiresAt": expiresAt}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetDeviceIdentityGrant(item.ID)
}

func (db *DB) GetDeviceIdentityGrant(id string) (*DeviceIdentityGrant, error) {
	item := &DeviceIdentityGrant{}
	var raw string
	var expires sql.NullTime
	err := db.QueryRow(`SELECT grant_record.id, grant_record.target_device_id, target.name,
		target.identity_id, target_identity.name, grant_record.grantee_identity_id, grantee.name,
		grant_record.features, grant_record.expires_at, grant_record.revision,
		grant_record.created_by, grant_record.updated_by, grant_record.created_at, grant_record.updated_at
		FROM device_identity_grants grant_record
		JOIN devices target ON target.id = grant_record.target_device_id
		JOIN identities target_identity ON target_identity.id = target.identity_id
		JOIN identities grantee ON grantee.id = grant_record.grantee_identity_id
		WHERE grant_record.id = ?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.TargetDeviceID, &item.TargetDeviceName,
		&item.TargetIdentityID, &item.TargetIdentityName, &item.GranteeIdentityID, &item.GranteeIdentityName,
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

func (db *DB) ListDeviceIdentityGrants(targetDeviceID, granteeIdentityID, feature string) ([]*DeviceIdentityGrant, error) {
	targetDeviceID = strings.TrimSpace(targetDeviceID)
	granteeIdentityID = strings.TrimSpace(granteeIdentityID)
	feature = strings.TrimSpace(feature)
	if feature != "" && feature != GrantFeatureProxyUse && feature != GrantFeatureRDPConnect {
		return nil, fmt.Errorf("unsupported grant feature %q", feature)
	}

	query := `SELECT grant_record.id, grant_record.target_device_id, target.name,
		target.identity_id, target_identity.name, grant_record.grantee_identity_id, grantee.name,
		grant_record.features, grant_record.expires_at, grant_record.revision,
		grant_record.created_by, grant_record.updated_by, grant_record.created_at, grant_record.updated_at
		FROM device_identity_grants grant_record
		JOIN devices target ON target.id = grant_record.target_device_id
		JOIN identities target_identity ON target_identity.id = target.identity_id
		JOIN identities grantee ON grantee.id = grant_record.grantee_identity_id
		WHERE 1 = 1`
	args := make([]any, 0, 2)
	if targetDeviceID != "" {
		query += ` AND grant_record.target_device_id = ?`
		args = append(args, targetDeviceID)
	}
	if granteeIdentityID != "" {
		query += ` AND grant_record.grantee_identity_id = ?`
		args = append(args, granteeIdentityID)
	}
	query += ` ORDER BY target.name, grantee.name, grant_record.id`

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*DeviceIdentityGrant, 0)
	for rows.Next() {
		item := &DeviceIdentityGrant{}
		var raw string
		var expires sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.TargetDeviceID, &item.TargetDeviceName,
			&item.TargetIdentityID, &item.TargetIdentityName, &item.GranteeIdentityID, &item.GranteeIdentityName,
			&raw, &expires, &item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Features, err = decodeGrantFeatures(raw)
		if err != nil {
			return nil, err
		}
		if feature != "" && !containsGrantFeature(item.Features, feature) {
			continue
		}
		if expires.Valid {
			value := expires.Time.UTC()
			item.ExpiresAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) UpdateDeviceIdentityGrant(id, actor string, update DeviceIdentityGrantUpdate) (*DeviceIdentityGrant, error) {
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

	var targetDeviceID, currentGrantee, raw string
	var currentExpires sql.NullTime
	var currentRevision int64
	if err := tx.QueryRow(`SELECT target_device_id, grantee_identity_id, features, expires_at, revision
		FROM device_identity_grants WHERE id = ?`, id).
		Scan(&targetDeviceID, &currentGrantee, &raw, &currentExpires, &currentRevision); err != nil {
		return nil, err
	}
	if update.Revision > 0 && update.Revision != currentRevision {
		return nil, ErrDeviceIdentityGrantRevision
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
	nextFeatures, err = db.validateDeviceIdentityGrant(tx, targetDeviceID, nextGrantee, nextFeatures)
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
		if err := tx.QueryRow(`SELECT COUNT(*) FROM device_identity_grants
			WHERE target_device_id = ? AND grantee_identity_id = ? AND id <> ?`,
			targetDeviceID, nextGrantee, id).Scan(&duplicate); err != nil {
			return nil, err
		}
		if duplicate != 0 {
			return nil, ErrDeviceIdentityGrantExists
		}
	}

	now := time.Now().UTC()
	nextRevision := currentRevision + 1
	result, err := tx.Exec(`UPDATE device_identity_grants
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
		return nil, ErrDeviceIdentityGrantRevision
	}
	if err := insertAuthorizationAudit(tx, "device_identity_grant.update", actor, "device_identity_grant", id,
		map[string]any{
			"before": map[string]any{"granteeIdentityId": currentGrantee, "features": currentFeatures, "revision": currentRevision},
			"after":  map[string]any{"granteeIdentityId": nextGrantee, "features": nextFeatures, "expiresAt": nextExpires, "revision": nextRevision},
		}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetDeviceIdentityGrant(id)
}

func (db *DB) DeleteDeviceIdentityGrant(id, actor string, revision int64) (bool, error) {
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
	var targetDeviceID, granteeIdentityID, raw string
	var currentRevision int64
	err = tx.QueryRow(`SELECT target_device_id, grantee_identity_id, features, revision
		FROM device_identity_grants WHERE id = ?`, id).
		Scan(&targetDeviceID, &granteeIdentityID, &raw, &currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if revision > 0 && revision != currentRevision {
		return false, ErrDeviceIdentityGrantRevision
	}
	features, err := decodeGrantFeatures(raw)
	if err != nil {
		return false, err
	}
	result, err := tx.Exec(`DELETE FROM device_identity_grants WHERE id = ? AND revision = ?`, id, currentRevision)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if rows != 1 {
		return false, ErrDeviceIdentityGrantRevision
	}
	now := time.Now().UTC()
	if err := insertAuthorizationAudit(tx, "device_identity_grant.delete", actor, "device_identity_grant", id,
		map[string]any{"targetDeviceId": targetDeviceID, "granteeIdentityId": granteeIdentityID, "features": features, "revision": currentRevision}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func containsGrantFeature(features []string, feature string) bool {
	for _, value := range features {
		if value == feature {
			return true
		}
	}
	return false
}

func (db *DB) HasActiveDeviceIdentityGrant(targetDeviceID, granteeIdentityID, feature string) (bool, error) {
	if targetDeviceID == "" || granteeIdentityID == "" {
		return false, nil
	}
	if feature != GrantFeatureProxyUse && feature != GrantFeatureRDPConnect {
		return false, fmt.Errorf("unsupported grant feature %q", feature)
	}
	rows, err := db.Query(`SELECT features FROM device_identity_grants
		WHERE target_device_id = ? AND grantee_identity_id = ?
		  AND (expires_at IS NULL OR expires_at > ?)`,
		targetDeviceID, granteeIdentityID, time.Now().UTC())
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

// authorizeIdentityDeviceFeature evaluates only the v4 identity model. Legacy
// owner semantics stay in the feature-specific wrappers during migration.
func (db *DB) authorizeIdentityDeviceFeature(clientDeviceID, targetDeviceID, feature string) (bool, bool, error) {
	if clientDeviceID == "" || targetDeviceID == "" || clientDeviceID == targetDeviceID {
		return false, false, nil
	}
	var clientIdentity, targetIdentity, clientStatus, targetStatus sql.NullString
	var clientCaps, targetCaps string
	err := db.QueryRow(`SELECT client.identity_id, client.approved_capabilities, client_identity.status,
		target.identity_id, target.approved_capabilities, target_identity.status
		FROM devices client
		JOIN devices target ON target.id = ?
		LEFT JOIN identities client_identity ON client_identity.id = client.identity_id
		LEFT JOIN identities target_identity ON target_identity.id = target.identity_id
		WHERE client.id = ? AND client.approval_state = 'approved' AND target.approval_state = 'approved'`,
		targetDeviceID, clientDeviceID).Scan(
		&clientIdentity, &clientCaps, &clientStatus, &targetIdentity, &targetCaps, &targetStatus)
	if err != nil {
		return false, false, err
	}
	if !clientIdentity.Valid && !targetIdentity.Valid {
		return false, false, nil
	}
	if !clientIdentity.Valid || !targetIdentity.Valid ||
		strings.TrimSpace(clientIdentity.String) == "" || strings.TrimSpace(targetIdentity.String) == "" {
		return true, false, nil
	}
	if clientStatus.String != IdentityStatusActive || targetStatus.String != IdentityStatusActive {
		return true, false, nil
	}

	switch feature {
	case GrantFeatureProxyUse:
		if !hasCapabilityJSON(clientCaps, "proxy.client") || !hasCapabilityJSON(targetCaps, "proxy.exit") {
			return true, false, nil
		}
	case GrantFeatureRDPConnect:
		if !hasCapabilityJSON(clientCaps, "rdp.controller") || !hasCapabilityJSON(targetCaps, "rdp.host") {
			return true, false, nil
		}
	default:
		return true, false, fmt.Errorf("unsupported grant feature %q", feature)
	}

	if clientIdentity.String == targetIdentity.String {
		return true, true, nil
	}
	granted, err := db.HasActiveDeviceIdentityGrant(targetDeviceID, clientIdentity.String, feature)
	return true, granted, err
}
