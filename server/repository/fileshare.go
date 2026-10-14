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
