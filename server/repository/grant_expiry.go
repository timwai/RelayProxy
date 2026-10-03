package repository

import (
	"encoding/json"
	"sort"
	"time"
)

// ExpireIdentityGrants atomically removes explicit grants whose deadline has
// passed and returns the grantee identities whose live sessions must be
// invalidated. New admission checks already reject an expired row before this
// cleanup runs; the sweep exists to bound the lifetime of already-open Relay
// streams that do not have a lease renewal handshake.
func (db *DB) ExpireIdentityGrants(now time.Time) ([]string, error) {
	now = now.UTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	affected := map[string]bool{}

	deviceRows, err := tx.Query(`SELECT id, target_device_id, grantee_identity_id, features, revision
		FROM device_identity_grants
		WHERE expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY id`, now)
	if err != nil {
		return nil, err
	}
	type deviceGrant struct {
		id, targetDeviceID, granteeIdentityID, raw string
		revision                             int64
	}
	expiredDevices := make([]deviceGrant, 0)
	for deviceRows.Next() {
		var item deviceGrant
		if err := deviceRows.Scan(&item.id, &item.targetDeviceID, &item.granteeIdentityID, &item.raw, &item.revision); err != nil {
			_ = deviceRows.Close()
			return nil, err
		}
		expiredDevices = append(expiredDevices, item)
	}
	if err := deviceRows.Err(); err != nil {
		_ = deviceRows.Close()
		return nil, err
	}
	_ = deviceRows.Close()

	for _, item := range expiredDevices {
		var features []string
		if err := json.Unmarshal([]byte(item.raw), &features); err != nil {
			return nil, err
		}
		result, err := tx.Exec(`DELETE FROM device_identity_grants
			WHERE id = ? AND expires_at IS NOT NULL AND expires_at <= ?`, item.id, now)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			continue
		}
		affected[item.granteeIdentityID] = true
		if err := insertAuthorizationAudit(tx, "device_identity_grant.expire", "system", "device_identity_grant", item.id,
			map[string]any{
				"targetDeviceId": item.targetDeviceID, "granteeIdentityId": item.granteeIdentityID,
				"features": features, "revision": item.revision,
			}, now); err != nil {
			return nil, err
		}
	}

	systemRows, err := tx.Query(`SELECT id, resource_id, grantee_identity_id, features, revision
		FROM system_identity_grants
		WHERE expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY id`, now)
	if err != nil {
		return nil, err
	}
	type systemGrant struct {
		id, resourceID, granteeIdentityID, raw string
		revision                         int64
	}
	expiredSystems := make([]systemGrant, 0)
	for systemRows.Next() {
		var item systemGrant
		if err := systemRows.Scan(&item.id, &item.resourceID, &item.granteeIdentityID, &item.raw, &item.revision); err != nil {
			_ = systemRows.Close()
			return nil, err
		}
		expiredSystems = append(expiredSystems, item)
	}
	if err := systemRows.Err(); err != nil {
		_ = systemRows.Close()
		return nil, err
	}
	_ = systemRows.Close()

	for _, item := range expiredSystems {
		var features []string
		if err := json.Unmarshal([]byte(item.raw), &features); err != nil {
			return nil, err
		}
		result, err := tx.Exec(`DELETE FROM system_identity_grants
			WHERE id = ? AND expires_at IS NOT NULL AND expires_at <= ?`, item.id, now)
		if err != nil {
			return nil, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if rows != 1 {
			continue
		}
		affected[item.granteeIdentityID] = true
		if err := insertAuthorizationAudit(tx, "system_identity_grant.expire", "system", "system_identity_grant", item.id,
			map[string]any{
				"resourceId": item.resourceID, "granteeIdentityId": item.granteeIdentityID,
				"features": features, "revision": item.revision,
			}, now); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(affected))
	for identityID := range affected {
		result = append(result, identityID)
	}
	sort.Strings(result)
	return result, nil
}
