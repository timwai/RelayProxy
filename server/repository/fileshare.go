package repository

import (
	"database/sql"
	"errors"
	"slices"

	"relayproxy/internal/protocol"
)

// AuthorizeFileShare deliberately uses persisted, current device grants,
// never a caller-supplied identity or a stale session permission snapshot.
// Phase 1 supports only devices in the same active identity. Local folder
// permissions are enforced independently on the destination Agent.
func (db *DB) AuthorizeFileShare(clientID, hostID string) (bool, error) {
	if clientID == "" || hostID == "" || clientID == hostID {
		return false, nil
	}
	var clientIdentity, hostIdentity, clientCaps, hostCaps string
	err := db.QueryRow(`SELECT COALESCE(client.identity_id,''), COALESCE(host.identity_id,''),
			client.approved_capabilities, host.approved_capabilities
		FROM devices client
		JOIN devices host ON host.id = ?
		JOIN identities ci ON ci.id = client.identity_id AND ci.status = 'active'
		JOIN identities hi ON hi.id = host.identity_id AND hi.status = 'active'
		WHERE client.id = ? AND client.approval_state = 'approved'
			AND host.approval_state = 'approved'`,
		hostID, clientID).Scan(&clientIdentity, &hostIdentity, &clientCaps, &hostCaps)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if clientIdentity == "" || hostIdentity != clientIdentity {
		return false, nil
	}
	clientGrants, err := decodeCapabilities(clientCaps)
	if err != nil {
		return false, err
	}
	hostGrants, err := decodeCapabilities(hostCaps)
	if err != nil {
		return false, err
	}
	return slices.Contains(clientGrants, protocol.CapabilityFilesClient) &&
		slices.Contains(hostGrants, protocol.CapabilityFilesHost), nil
}

// ListFileShareCandidates enumerates approved files.client devices in the
// requesting host's active identity. It intentionally includes offline peers,
// never exposes members of a different identity, and does not grant access.
func (db *DB) ListFileShareCandidates(hostDeviceID string) ([]protocol.FileShareCandidate, error) {
	result := []protocol.FileShareCandidate{}
	if hostDeviceID == "" {
		return result, nil
	}
	var approved string
	var identityID string
	err := db.QueryRow(`SELECT h.approved_capabilities, h.identity_id
		FROM devices h
		JOIN identities ident ON ident.id = h.identity_id AND ident.status = 'active'
		WHERE h.id = ? AND h.approval_state = 'approved'`, hostDeviceID).Scan(&approved, &identityID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	hostGrants, err := decodeCapabilities(approved)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(hostGrants, protocol.CapabilityFilesHost) {
		return result, nil
	}
	rows, err := db.Query(`SELECT id, name, approved_capabilities
		FROM devices
		WHERE identity_id = ? AND approval_state = 'approved' AND id <> ?
		ORDER BY name, id LIMIT 512`, identityID, hostDeviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, raw string
		if err := rows.Scan(&id, &name, &raw); err != nil {
			return nil, err
		}
		grants, err := decodeCapabilities(raw)
		if err != nil {
			return nil, err
		}
		if slices.Contains(grants, protocol.CapabilityFilesClient) {
			result = append(result, protocol.FileShareCandidate{DeviceID: id, Name: name})
		}
	}
	return result, rows.Err()
}
