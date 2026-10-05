package repository

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type ProxyDirectAuthorizationContext struct {
	PolicyRevision        int64
	AuthorizationRevision int64
}

func (db *DB) PublicDirectAuthorizationContext(clientDeviceID, exitDeviceID string) (ProxyDirectAuthorizationContext, bool, error) {
	clientDeviceID = strings.TrimSpace(clientDeviceID)
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if clientDeviceID == "" || exitDeviceID == "" || clientDeviceID == exitDeviceID ||
		exitDeviceID == SystemResourceServerExit {
		return ProxyDirectAuthorizationContext{}, false, nil
	}

	allowed, err := db.AuthorizeClientExit(clientDeviceID, exitDeviceID)
	if err != nil || !allowed {
		return ProxyDirectAuthorizationContext{}, allowed, err
	}

	var (
		clientIdentity sql.NullString
		exitIdentity   sql.NullString
		clientOwner    sql.NullString
		exitOwner      sql.NullString
		exitPolicy     sql.NullInt64
		exitStatus     sql.NullString
	)
	err = db.QueryRow(`SELECT client.identity_id, exit.identity_id,
		client.owner_user_id, exit.owner_user_id,
		exit_identity.policy_revision, exit_identity.status
		FROM devices client
		JOIN devices exit ON exit.id = ?
		LEFT JOIN identities exit_identity ON exit_identity.id = exit.identity_id
		WHERE client.id = ? AND client.approval_state = ? AND exit.approval_state = ?`,
		exitDeviceID, clientDeviceID, EnrollmentApproved, EnrollmentApproved).
		Scan(&clientIdentity, &exitIdentity, &clientOwner, &exitOwner, &exitPolicy, &exitStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ProxyDirectAuthorizationContext{}, false, nil
	}
	if err != nil {
		return ProxyDirectAuthorizationContext{}, false, err
	}

	if clientIdentity.Valid || exitIdentity.Valid {
		if !clientIdentity.Valid || !exitIdentity.Valid ||
			strings.TrimSpace(clientIdentity.String) == "" ||
			strings.TrimSpace(exitIdentity.String) == "" ||
			!exitPolicy.Valid || exitStatus.String != IdentityStatusActive {
			return ProxyDirectAuthorizationContext{}, false, nil
		}
		context := ProxyDirectAuthorizationContext{PolicyRevision: exitPolicy.Int64}
		if clientIdentity.String == exitIdentity.String {
			return context, true, nil
		}

		rows, err := db.Query(`SELECT features, revision
			FROM device_identity_grants
			WHERE target_device_id = ? AND grantee_identity_id = ?
			  AND (expires_at IS NULL OR expires_at > ?)`,
			exitDeviceID, clientIdentity.String, time.Now().UTC())
		if err != nil {
			return ProxyDirectAuthorizationContext{}, false, err
		}
		defer rows.Close()
		for rows.Next() {
			var raw string
			var revision int64
			if err := rows.Scan(&raw, &revision); err != nil {
				return ProxyDirectAuthorizationContext{}, false, err
			}
			features, err := decodeGrantFeatures(raw)
			if err != nil {
				return ProxyDirectAuthorizationContext{}, false, err
			}
			if containsGrantFeature(features, GrantFeatureProxyUse) {
				context.AuthorizationRevision = revision
				return context, true, nil
			}
		}
		if err := rows.Err(); err != nil {
			return ProxyDirectAuthorizationContext{}, false, err
		}
		return ProxyDirectAuthorizationContext{}, false, nil
	}

	if clientOwner.Valid && exitOwner.Valid && clientOwner.String != "" && clientOwner.String == exitOwner.String {
		return ProxyDirectAuthorizationContext{}, true, nil
	}
	return ProxyDirectAuthorizationContext{}, false, nil
}


func (db *DB) DeviceIDsForIdentity(identityID string) ([]string, error) {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return []string{}, nil
	}
	rows, err := db.Query(`SELECT id FROM devices WHERE identity_id = ? ORDER BY id`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			return nil, err
		}
		if deviceID = strings.TrimSpace(deviceID); deviceID != "" {
			result = append(result, deviceID)
		}
	}
	return result, rows.Err()
}
