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
var ErrDeleteRequiresRevoke = errors.New("revoke browser authorization before deleting the record")
var ErrExtensionStillLinked = errors.New("delete linked browser devices before deleting the extension")

// BrowserDevice is entirely separate from the privileged RelayProxy Agent model.
type BrowserDevice struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SigningKey     string    `json:"signingPublicKey"`
	EncryptionKey  string    `json:"encryptionPublicKey"`
	IdentityID     string    `json:"identityId,omitempty"`
	RequestedIdentityID string `json:"requestedIdentityId,omitempty"`
	State          string    `json:"state"`
	Send           bool      `json:"send"`
	Receive        bool      `json:"receive"`
	CreatedAt      time.Time `json:"createdAt"`
	ExtensionID    string    `json:"extensionId,omitempty"`
	ExtensionState string    `json:"extensionState,omitempty"`
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
		`CREATE TABLE IF NOT EXISTS browser_sync_identity_requests (device_id TEXT PRIMARY KEY REFERENCES browser_sync_devices(id) ON DELETE CASCADE, requested_identity_id TEXT NOT NULL)`,
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
	store := &Store{db: db}
	if err := store.EnsureExtensionSchema(ctx); err != nil {
		return nil, fmt.Errorf("browser extension schema: %w", err)
	}
	if err := store.EnsureRuleSchema(ctx); err != nil {
		return nil, fmt.Errorf("browser sync rule schema: %w", err)
	}
	if err := store.EnsureSessionSchema(ctx); err != nil {
		return nil, fmt.Errorf("browser sync replay schema: %w", err)
	}
	if err := store.EnsureDeliverySchema(ctx); err != nil {
		return nil, fmt.Errorf("browser sync delivery schema: %w", err)
	}
	if err := store.EnsureNetworkSchema(ctx); err != nil {
		return nil, fmt.Errorf("browser sync network schema: %w", err)
	}
	return store, nil
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
	if device.RequestedIdentityID != "" && actual.State == "pending" {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO browser_sync_identity_requests(device_id,requested_identity_id) VALUES(?,?) ON CONFLICT(device_id) DO UPDATE SET requested_identity_id=excluded.requested_identity_id`, device.ID, device.RequestedIdentityID); err != nil { return BrowserDevice{}, err }
		actual.RequestedIdentityID = device.RequestedIdentityID
	}
	return actual, nil
}
func (s *Store) Device(ctx context.Context, id string) (BrowserDevice, error) {
	var d BrowserDevice
	var send, receive int
	err := s.db.QueryRowContext(ctx, `SELECT d.id,d.name,d.signing_key,d.encryption_key,d.identity_id,d.state,
    d.can_send,d.can_receive,d.created_at,COALESCE(x.extension_id,''),COALESCE(e.state,''),COALESCE(ri.requested_identity_id,'')
    FROM browser_sync_devices d
    LEFT JOIN browser_sync_device_extensions x ON x.device_id=d.id
    LEFT JOIN browser_sync_extensions e ON e.extension_id=x.extension_id
    LEFT JOIN browser_sync_identity_requests ri ON ri.device_id=d.id
    WHERE d.id=?`, id).
		Scan(&d.ID, &d.Name, &d.SigningKey, &d.EncryptionKey, &d.IdentityID, &d.State, &send, &receive, &d.CreatedAt, &d.ExtensionID, &d.ExtensionState, &d.RequestedIdentityID)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserDevice{}, ErrNotFound
	}
	d.Send = send == 1
	d.Receive = receive == 1
	return d, err
}
func (s *Store) Devices(ctx context.Context) ([]BrowserDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM browser_sync_devices
  ORDER BY CASE state WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END,
           updated_at DESC LIMIT 500`)
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
// DeleteRevokedDevice permanently removes a revoked browser and every
// dependent session/rule record in one transaction. Revocation remains a
// separate explicit admin action: a pending or approved browser can never
// be silently deleted to circumvent approval/audit controls.
func (s *Store) DeleteRevokedDevice(ctx context.Context, id string) error {
	if !deviceIDPattern.MatchString(id) {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, "SELECT state FROM browser_sync_devices WHERE id=?", id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != "revoked" {
		return ErrDeleteRequiresRevoke
	}
	// Explicit cleanup works even on older SQLite connections where foreign
	// key enforcement might not be enabled. All rule dependencies are removed
	// before deleting the rules and their source/target browser device.
	queries := []string{
		`DELETE FROM browser_sync_deliveries WHERE source_id=? OR target_id=?
       OR rule_id IN (SELECT id FROM browser_sync_rules WHERE source_id=? OR target_id=?)`,
		`DELETE FROM browser_sync_delivery_results WHERE source_id=?
       OR rule_id IN (SELECT id FROM browser_sync_rules WHERE source_id=? OR target_id=?)`,
		`DELETE FROM browser_sync_network_bindings WHERE rule_id IN
       (SELECT id FROM browser_sync_rules WHERE source_id=? OR target_id=?)`,
		`DELETE FROM browser_sync_sequences WHERE rule_id IN
       (SELECT id FROM browser_sync_rules WHERE source_id=? OR target_id=?)`,
		`DELETE FROM browser_sync_rule_offers WHERE rule_id IN
       (SELECT id FROM browser_sync_rules WHERE source_id=? OR target_id=?)`,
		`DELETE FROM browser_sync_rules WHERE source_id=? OR target_id=?`,
		`DELETE FROM browser_sync_device_extensions WHERE device_id=?`,
		`DELETE FROM browser_sync_devices WHERE id=? AND state='revoked'`,
	}
	args := [][]any{
		{id, id, id, id}, {id, id, id}, {id, id}, {id, id}, {id, id},
		{id, id}, {id}, {id},
	}
	for i, query := range queries {
		result, err := tx.ExecContext(ctx, query, args[i]...)
		if err != nil {
			return fmt.Errorf("delete browser device dependencies: %w", err)
		}
		if i == len(queries)-1 {
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrNotFound
			}
		}
	}
	return tx.Commit()
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
