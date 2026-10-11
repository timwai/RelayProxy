package browsersync

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var chromeExtensionIDPattern = regexp.MustCompile(`^[a-p]{32}$`)

// BrowserExtension is a separately reviewed Chrome origin, NOT an Agent or
// an approved BrowserDevice. Unknown origins can only queue an approval request.
type BrowserExtension struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) EnsureExtensionSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_extensions (
		extension_id TEXT PRIMARY KEY, state TEXT NOT NULL DEFAULT 'pending',
		created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
		CHECK(state IN ('pending','approved','revoked'))
	)`); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS browser_sync_device_extensions (
        device_id TEXT PRIMARY KEY REFERENCES browser_sync_devices(id),
        extension_id TEXT NOT NULL REFERENCES browser_sync_extensions(extension_id),
        created_at TIMESTAMP NOT NULL
    )`)
	return err
}

func (s *Store) RequestExtension(ctx context.Context, id string) (string, error) {
	if !chromeExtensionIDPattern.MatchString(id) {
		return "", errors.New("invalid Chrome extension ID")
	}
	// Bound the persistent unauthenticated queue independently from IP rate limits.
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_sync_extensions`).Scan(&count); err != nil {
		return "", err
	}
	var state string
	err := s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if count >= 500 {
		return "", errors.New("browser extension approval queue full")
	}
	now := time.Now().UTC()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO browser_sync_extensions(extension_id,state,created_at,updated_at)
		VALUES(?,'pending',?,?) ON CONFLICT(extension_id) DO NOTHING`, id, now, now); err != nil {
		return "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state)
	return state, err
}

func (s *Store) ExtensionApproved(ctx context.Context, id string) bool {
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, id).Scan(&state); err != nil {
		return false
	}
	return state == "approved"
}

func (s *Store) Extensions(ctx context.Context) ([]BrowserExtension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT extension_id,state,created_at FROM browser_sync_extensions ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []BrowserExtension{}
	for rows.Next() {
		var item BrowserExtension
		if err := rows.Scan(&item.ID, &item.State, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetExtensionApproved(ctx context.Context, id string, approve bool) error {
	if !chromeExtensionIDPattern.MatchString(id) {
		return ErrNotFound
	}
	state := "revoked"
	if approve {
		state = "approved"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_extensions SET state=?,updated_at=?
		WHERE extension_id=? AND state=?`, state, time.Now().UTC(), id, func() string {
		if approve {
			return "pending"
		}
		return "approved"
	}())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("extension approval transition denied: %w", ErrNotFound)
	}
	return nil
}


// EnrollExtensionDevice binds a Chrome Origin to the exact device ID and
// P-256 public keys during a single, unprivileged registration attempt.
// Approval remains an independent admin-only transaction.
func (s *Store) EnrollExtensionDevice(ctx context.Context, extensionID string, device BrowserDevice) (BrowserDevice, string, error) {
	if err := ValidateRegistration(device); err != nil {
		return BrowserDevice{}, "", err
	}
	if device.State != "" || device.IdentityID != "" || device.Send || device.Receive ||
		!device.CreatedAt.IsZero() || device.ExtensionID != "" || device.ExtensionState != "" {
		return BrowserDevice{}, "", errors.New("browser registration cannot set privileges")
	}
	state, err := s.RequestExtension(ctx, extensionID)
	if err != nil {
		return BrowserDevice{}, "", err
	}
	if state == "revoked" {
		return BrowserDevice{}, "", ErrNotFound
	}
	// Registration cannot assign an identity or a capability.
	// The device key check in Register denies changed key material.
	saved, err := s.Register(ctx, device)
	if err != nil {
		return BrowserDevice{}, "", err
	}
	// A previously approved legacy device cannot be rebound to a newly
	// unapproved extension using public keys alone.
	if saved.State == "approved" && state != "approved" {
		return BrowserDevice{}, "", ErrDeviceConflict
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO browser_sync_device_extensions(device_id,extension_id,created_at)
        VALUES(?,?,?) ON CONFLICT(device_id) DO NOTHING`, device.ID, extensionID, time.Now().UTC())
	if err != nil {
		return BrowserDevice{}, "", err
	}
	var linked string
	if err := s.db.QueryRowContext(ctx, `SELECT extension_id FROM browser_sync_device_extensions
        WHERE device_id=?`, device.ID).Scan(&linked); err != nil {
		return BrowserDevice{}, "", err
	}
	if linked != extensionID {
		return BrowserDevice{}, "", ErrDeviceConflict
	}
	saved.ExtensionID, saved.ExtensionState = extensionID, state
	return saved, state, nil
}

// ApproveEnrollment approves the linked extension and pending device atomically.
// Legacy devices without an extension binding keep the old approval path.
func (s *Store) ApproveEnrollment(ctx context.Context, id, identityID string, canSend, canReceive bool) error {
	var extensionID string
	err := s.db.QueryRowContext(ctx, `SELECT extension_id FROM browser_sync_device_extensions
        WHERE device_id=?`, id).Scan(&extensionID)
	if errors.Is(err, sql.ErrNoRows) {
		return s.Approve(ctx, id, identityID, canSend, canReceive)
	}
	if err != nil {
		return err
	}
	if id == "" || identityID == "" || (!canSend && !canReceive) {
		return errors.New("invalid approval")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var identityState, extensionState string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM identities WHERE id=?`, identityID).Scan(&identityState); err != nil || identityState != "active" {
		return errors.New("identity not active")
	}
	if err := tx.QueryRowContext(ctx, `SELECT state FROM browser_sync_extensions WHERE extension_id=?`, extensionID).Scan(&extensionState); err != nil {
		return err
	}
	if extensionState != "pending" && extensionState != "approved" {
		return ErrNotFound // revoked extensions cannot be enrolled
	}
	result, err := tx.ExecContext(ctx, `UPDATE browser_sync_devices
        SET identity_id=?,state='approved',can_send=?,can_receive=?,updated_at=?
        WHERE id=? AND state='pending'`, identityID, boolInt(canSend), boolInt(canReceive), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	if extensionState == "pending" {
		result, err = tx.ExecContext(ctx, `UPDATE browser_sync_extensions SET state='approved', updated_at=?
            WHERE extension_id=? AND state='pending'`, time.Now().UTC(), extensionID)
		if err != nil {
			return err
		}
		n, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
	}
	return tx.Commit()
}
