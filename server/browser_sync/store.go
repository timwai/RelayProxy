package browsersync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("browser device not found")
var ErrDeviceConflict = errors.New("browser device key mismatch")

// BrowserDevice is entirely separate from the privileged RelayProxy Agent model.
type BrowserDevice struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SigningKey    string    `json:"signingPublicKey"`
	EncryptionKey string    `json:"encryptionPublicKey"`
	IdentityID    string    `json:"identityId,omitempty"`
	State         string    `json:"state"`
	Send          bool      `json:"send"`
	Receive       bool      `json:"receive"`
	CreatedAt     time.Time `json:"createdAt"`
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("missing browser sync database")
	}
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS browser_sync_devices (
    id TEXT PRIMARY KEY, name TEXT NOT NULL, signing_key TEXT NOT NULL, encryption_key TEXT NOT NULL,
    identity_id TEXT NOT NULL DEFAULT '', state TEXT NOT NULL DEFAULT 'pending',
    can_send INTEGER NOT NULL DEFAULT 0, can_receive INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
    CHECK (state IN ('pending','approved','revoked'))
   )`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_browser_signing_key ON browser_sync_devices(signing_key)`,
		`CREATE TABLE IF NOT EXISTS browser_sync_rules (
    id TEXT PRIMARY KEY, source_id TEXT NOT NULL REFERENCES browser_sync_devices(id),
    target_id TEXT NOT NULL REFERENCES browser_sync_devices(id),
    policy_digest TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'offered',
    source_approved INTEGER NOT NULL DEFAULT 0, target_approved INTEGER NOT NULL DEFAULT 0,
    keys_confirmed INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
    CHECK(status IN ('offered','active','paused','revoked'))
   )`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, query := range ddl {
		if _, err := db.ExecContext(ctx, query); err != nil {
			return nil, fmt.Errorf("browser sync schema: %w", err)
		}
	}
	return &Store{db: db}, nil
}
func (s *Store) Register(ctx context.Context, device BrowserDevice) (BrowserDevice, error) {
	if device.ID == "" || device.Name == "" || len(device.Name) > 100 || len(device.SigningKey) > 1024 || len(device.EncryptionKey) > 1024 {
		return BrowserDevice{}, errors.New("invalid browser registration")
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO browser_sync_devices(
   id,name,signing_key,encryption_key,created_at,updated_at
 ) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, device.ID, device.Name, device.SigningKey, device.EncryptionKey, now, now)
	if err != nil {
		return BrowserDevice{}, err
	}
	actual, err := s.Device(ctx, device.ID)
	if err != nil {
		return BrowserDevice{}, err
	}
	if actual.SigningKey != device.SigningKey || actual.EncryptionKey != device.EncryptionKey {
		return BrowserDevice{}, ErrDeviceConflict
	}
	return actual, nil
}
func (s *Store) Device(ctx context.Context, id string) (BrowserDevice, error) {
	var d BrowserDevice
	var send, receive int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,signing_key,encryption_key,identity_id,state,can_send,can_receive,created_at FROM browser_sync_devices WHERE id=?`, id).
		Scan(&d.ID, &d.Name, &d.SigningKey, &d.EncryptionKey, &d.IdentityID, &d.State, &send, &receive, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserDevice{}, ErrNotFound
	}
	d.Send = send == 1
	d.Receive = receive == 1
	return d, err
}
func (s *Store) Devices(ctx context.Context) ([]BrowserDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM browser_sync_devices ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]BrowserDevice, 0, len(ids))
	for _, id := range ids {
		d, err := s.Device(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

// Approve requires a separately authenticated administrator. It never confers
// ordinary Agent proxy/RDP capabilities; the identity must exist and be active.
func (s *Store) Approve(ctx context.Context, id, identityID string, canSend, canReceive bool) error {
	if id == "" || identityID == "" || (!canSend && !canReceive) {
		return errors.New("invalid approval")
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM identities WHERE id=?`, identityID).Scan(&status); err != nil {
		return errors.New("identity does not exist")
	}
	if status != "active" {
		return errors.New("identity is not active")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_devices SET identity_id=?,state='approved',can_send=?,can_receive=?,updated_at=? WHERE id=? AND state='pending'`,
		identityID, boolInt(canSend), boolInt(canReceive), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) Revoke(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE browser_sync_devices SET state='revoked', can_send=0, can_receive=0,updated_at=? WHERE id=?`, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	// Invalidate pending/active rules as part of the same authorization sweep.
	_, err = s.db.ExecContext(ctx, `UPDATE browser_sync_rules SET status='revoked', updated_at=? WHERE source_id=? OR target_id=?`, time.Now().UTC(), id, id)
	return err
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func KeyFingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return hex.EncodeToString(sum[:])
}
