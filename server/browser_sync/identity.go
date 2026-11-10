package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// resolveApprovalIdentity accepts the public connection ID or a legacy internal
// ID, but always returns the internal ID used by authorization and quotas.
// Resolve and validate the saved request in the approval transaction so an
// administrator cannot substitute a different identity for the enrolled one.
func resolveApprovalIdentity(ctx context.Context, tx *sql.Tx, deviceID, reference string) (string, error) {
	var identityID, shortID, status string
	err := tx.QueryRowContext(ctx, `SELECT id, COALESCE(short_id,''), status FROM identities
		WHERE id=? OR short_id=?`, reference, strings.ToLower(strings.TrimSpace(reference))).
		Scan(&identityID, &shortID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("identity does not exist")
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", errors.New("identity is not active")
	}
	var requestedIdentity string
	err = tx.QueryRowContext(ctx, `SELECT requested_identity_id FROM browser_sync_identity_requests
		WHERE device_id=?`, deviceID).Scan(&requestedIdentity)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && requestedIdentity != identityID &&
		(shortID == "" || strings.ToLower(strings.TrimSpace(requestedIdentity)) != shortID) {
		return "", errors.New("approval identity differs from device enrollment request")
	}
	return identityID, nil
}
