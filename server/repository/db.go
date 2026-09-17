package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
}

const (
	SchemaGeneration = "relayproxy-server-rdp-v3"
	SchemaVersion    = 3
)

var ErrIncompatibleDatabase = errors.New("database belongs to an unsupported RelayProxy generation")

func OpenDB(driver, dsn string) (*DB, error) {
	isSQLite := driver == "sqlite" || driver == "sqlite3"
	if isSQLite {
		driver = "sqlite"
		dsn = ensureSQLiteDSN(dsn)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db failed: %w", err)
	}

	if isSQLite {
		// Single writer connection avoids SQLITE_BUSY under concurrent audit/last_seen writes.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxLifetime(0)
	}

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pingCancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db failed: %w", err)
	}

	if isSQLite {
		// Compatibility is checked before persistent pragmas such as WAL are applied.
		// Unsupported databases must be rejected without changing their file format.
		if err := validateSQLiteGeneration(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("db generation check failed: %w", err)
		}
		if err := applySQLitePragmas(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite pragma failed: %w", err)
		}
	}

	wrapper := &DB{DB: db}
	if err := wrapper.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db migration failed: %w", err)
	}

	return wrapper, nil
}

func ensureSQLiteDSN(dsn string) string {
	if dsn == "" {
		dsn = "relayproxy.db"
	}
	if strings.Contains(dsn, "://") || strings.HasPrefix(dsn, "file:") {
		return dsn
	}
	// Prefer URI form so query pragmas are accepted by modernc.org/sqlite.
	if strings.Contains(dsn, "?") {
		return "file:" + dsn
	}
	return "file:" + dsn
}

func validateSQLiteGeneration(db *sql.DB) error {
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount == 0 {
		return nil
	}

	var generation string
	var version int
	err := db.QueryRow(`SELECT generation, schema_version FROM schema_meta WHERE id = 1`).Scan(&generation, &version)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") || errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: existing database has no schema_meta; move it aside and start with a new database", ErrIncompatibleDatabase)
		}
		return err
	}
	if generation != SchemaGeneration || version != SchemaVersion {
		return fmt.Errorf("%w: found generation=%q version=%d, require generation=%q version=%d",
			ErrIncompatibleDatabase, generation, version, SchemaGeneration, SchemaVersion)
	}
	return nil
}

func applySQLitePragmas(db *sql.DB) error {
	pragmas := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA synchronous = NORMAL`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func (db *DB) migrate() error {
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		return err
	}
	if tableCount > 0 {
		return validateSQLiteGeneration(db.DB)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	queries := []string{
		`CREATE TABLE schema_meta (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			generation VARCHAR(100) NOT NULL,
			schema_version INTEGER NOT NULL,
			server_instance_id VARCHAR(64) NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id VARCHAR(36) PRIMARY KEY,
			username VARCHAR(100) NOT NULL UNIQUE,
			password_hash VARCHAR(255) NOT NULL,
			display_name VARCHAR(100),
			role VARCHAR(30) NOT NULL,
			status VARCHAR(20) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE devices (
			id VARCHAR(36) PRIMARY KEY,
			owner_user_id VARCHAR(36),
			name VARCHAR(100) NOT NULL,
			public_key_fingerprint VARCHAR(64) NOT NULL UNIQUE,
			installation_id VARCHAR(64) NOT NULL,
			platform VARCHAR(30),
			arch VARCHAR(30),
			client_version VARCHAR(30),
			approval_state VARCHAR(20) NOT NULL,
			requested_capabilities TEXT NOT NULL,
			approved_capabilities TEXT NOT NULL,
			last_seen_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(owner_user_id) REFERENCES users(id)
		)`,
		`CREATE TABLE device_identities (
			fingerprint VARCHAR(64) PRIMARY KEY,
			device_id VARCHAR(36) UNIQUE,
			installation_id VARCHAR(64) NOT NULL,
			public_key BLOB NOT NULL,
			status VARCHAR(20) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(device_id) REFERENCES devices(id)
		)`,
		`CREATE TABLE device_enrollment_requests (
			id VARCHAR(36) PRIMARY KEY,
			fingerprint VARCHAR(64) NOT NULL UNIQUE,
			installation_id VARCHAR(64) NOT NULL,
			public_key BLOB NOT NULL,
			device_name VARCHAR(100) NOT NULL,
			platform VARCHAR(30),
			arch VARCHAR(30),
			client_version VARCHAR(30),
			requested_capabilities TEXT NOT NULL,
			state VARCHAR(20) NOT NULL,
			first_seen_at TIMESTAMP NOT NULL,
			last_seen_at TIMESTAMP NOT NULL,
			reviewed_at TIMESTAMP,
			reviewed_by VARCHAR(36),
			rejection_reason VARCHAR(255)
		)`,
		`CREATE TABLE device_grants (
			device_id VARCHAR(36) NOT NULL,
			capability VARCHAR(100) NOT NULL,
			granted_by VARCHAR(36) NOT NULL,
			granted_at TIMESTAMP NOT NULL,
			PRIMARY KEY(device_id, capability),
			FOREIGN KEY(device_id) REFERENCES devices(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE rdp_access_grants (
			controller_device_id VARCHAR(36) NOT NULL,
			target_device_id VARCHAR(36) NOT NULL,
			granted_by VARCHAR(36) NOT NULL,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY(controller_device_id, target_device_id),
			FOREIGN KEY(controller_device_id) REFERENCES devices(id) ON DELETE CASCADE,
			FOREIGN KEY(target_device_id) REFERENCES devices(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE authorization_audit (
			id VARCHAR(36) PRIMARY KEY,
			action VARCHAR(40) NOT NULL,
			actor_user_id VARCHAR(36) NOT NULL,
			target_type VARCHAR(30) NOT NULL,
			target_id VARCHAR(100) NOT NULL,
			details TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE connection_audit (
			id VARCHAR(36) PRIMARY KEY,
			user_id VARCHAR(36),
			client_device_id VARCHAR(36),
			exit_device_id VARCHAR(36),
			protocol VARCHAR(10),
			target_host VARCHAR(255),
			target_port INTEGER,
			resolved_ip VARCHAR(100),
			started_at TIMESTAMP,
			ended_at TIMESTAMP,
			bytes_up BIGINT DEFAULT 0,
			bytes_down BIGINT DEFAULT 0,
			result VARCHAR(50),
			error_code VARCHAR(50)
		)`,
		`CREATE TABLE rdp_services (
			id VARCHAR(36) PRIMARY KEY,
			device_id VARCHAR(36) NOT NULL,
			name VARCHAR(100) NOT NULL,
			target_host VARCHAR(255) NOT NULL,
			target_port INTEGER NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(device_id) REFERENCES devices(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE rdp_port_allocations (
			id VARCHAR(36) PRIMARY KEY,
			service_id VARCHAR(36) NOT NULL UNIQUE,
			listen_port INTEGER NOT NULL UNIQUE,
			status VARCHAR(20) NOT NULL,
			source_cidrs TEXT NOT NULL DEFAULT '[]',
			expires_at TIMESTAMP,
			rate_limit_per_min INTEGER NOT NULL DEFAULT 120,
			created_by VARCHAR(36),
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(service_id) REFERENCES rdp_services(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_connection_audit_started_at ON connection_audit(started_at)`,
		`CREATE INDEX IF NOT EXISTS idx_connection_audit_client_started ON connection_audit(client_device_id, started_at)`,
		`CREATE INDEX idx_devices_owner ON devices(owner_user_id)`,
		`CREATE INDEX idx_enrollments_state_seen ON device_enrollment_requests(state, last_seen_at)`,
		`CREATE INDEX idx_authorization_audit_created ON authorization_audit(created_at)`,
		`CREATE INDEX idx_rdp_access_target ON rdp_access_grants(target_device_id)`,
		`CREATE INDEX idx_rdp_services_device_enabled ON rdp_services(device_id, enabled)`,
	}

	for _, q := range queries {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_meta (id, generation, schema_version, server_instance_id, created_at)
		VALUES (1, ?, ?, ?, ?)`, SchemaGeneration, SchemaVersion, uuid.NewString(), time.Now().UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[DB] Initialized database generation %s schema %d", SchemaGeneration, SchemaVersion)
	return nil
}

// User model
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	DisplayName  string    `json:"displayName"`
	Role         string    `json:"role"` // "admin", "user"
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Device model
type Device struct {
	ID                    string     `json:"id"`
	OwnerUserID           string     `json:"ownerUserId"`
	Name                  string     `json:"name"`
	Fingerprint           string     `json:"fingerprint"`
	InstallationID        string     `json:"installationId"`
	Platform              string     `json:"platform"`
	Arch                  string     `json:"arch"`
	ClientVersion         string     `json:"clientVersion"`
	ApprovalState         string     `json:"approvalState"`
	RequestedCapabilities []string   `json:"requestedCapabilities"`
	ApprovedCapabilities  []string   `json:"approvedCapabilities"`
	LastSeenAt            *time.Time `json:"lastSeenAt"`
	CreatedAt             time.Time  `json:"createdAt"`
	UpdatedAt             time.Time  `json:"updatedAt"`

	// DeviceMode and Enabled are derived compatibility views for the existing
	// proxy/session UI. They are not persisted and never participate in auth.
	DeviceMode string `json:"deviceMode"`
	Enabled    bool   `json:"enabled"`
}

func (db *DB) CreateUser(u *User) error {
	if u.ID == "" {
		u.ID = uuid.New().String()
	}
	now := time.Now()
	u.CreatedAt = now
	u.UpdatedAt = now
	_, err := db.Exec(`INSERT INTO users (id, username, password_hash, display_name, role, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.DisplayName, u.Role, u.Status, u.CreatedAt, u.UpdatedAt)
	return err
}

func (db *DB) GetUserByUsername(username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(`SELECT id, username, password_hash, display_name, role, status, created_at, updated_at
		FROM users WHERE username = ?`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetUserByID reads the current role and account state for an authenticated request.
// Authorization must not depend on the role cached when the session was created.
func (db *DB) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := db.QueryRow(`SELECT id, username, display_name, role, status, created_at, updated_at
		FROM users WHERE id = ?`, id).Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetActiveUserPasswordHash is used only when checking account credentials.
func (db *DB) GetActiveUserPasswordHash(id string) (string, error) {
	var hash string
	err := db.QueryRow(`SELECT password_hash FROM users WHERE id = ? AND status = 'active'`, id).Scan(&hash)
	return hash, err
}

// UpdateUserPassword replaces exactly the credentials that were verified. A
// concurrent password change or account disable must not be overwritten.
func (db *DB) UpdateUserPassword(id, previousHash, newHash string) (bool, error) {
	result, err := db.Exec(`UPDATE users SET password_hash = ?, updated_at = ?
		WHERE id = ? AND password_hash = ? AND status = 'active'`, newHash, time.Now(), id, previousHash)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (db *DB) UpsertDevice(d *Device) error {
	now := time.Now()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.Fingerprint == "" {
		d.Fingerprint = "seed:" + d.ID
	}
	if d.InstallationID == "" {
		d.InstallationID = d.ID
	}
	if d.ApprovalState == "" {
		d.ApprovalState = "approved"
	}
	if len(d.ApprovedCapabilities) == 0 {
		d.ApprovedCapabilities = capabilitiesForMode(d.DeviceMode)
	}
	if len(d.RequestedCapabilities) == 0 {
		d.RequestedCapabilities = append([]string(nil), d.ApprovedCapabilities...)
	}
	d.UpdatedAt = now
	requested, err := encodeCapabilities(d.RequestedCapabilities)
	if err != nil {
		return err
	}
	approved, err := encodeCapabilities(d.ApprovedCapabilities)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO devices (id, owner_user_id, name, public_key_fingerprint, installation_id, platform, arch, client_version, approval_state, requested_capabilities, approved_capabilities, last_seen_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			owner_user_id = excluded.owner_user_id,
			platform = excluded.platform,
			arch = excluded.arch,
			client_version = excluded.client_version,
			approval_state = excluded.approval_state,
			requested_capabilities = excluded.requested_capabilities,
			approved_capabilities = excluded.approved_capabilities,
			last_seen_at = excluded.last_seen_at,
			updated_at = excluded.updated_at`,
		d.ID, nullableString(d.OwnerUserID), d.Name, d.Fingerprint, d.InstallationID, d.Platform, d.Arch,
		d.ClientVersion, d.ApprovalState, requested, approved, d.LastSeenAt, d.CreatedAt, d.UpdatedAt)
	return err
}

func (db *DB) ListDevices() ([]*Device, error) {
	return db.ListDevicesForOwner("")
}

// ListDevicesForOwner scopes ordinary users at the query boundary. An empty
// owner is reserved for callers that have already established administrator access.
func (db *DB) ListDevicesForOwner(ownerID string) ([]*Device, error) {
	query := `SELECT id, owner_user_id, name, public_key_fingerprint, installation_id, platform, arch, client_version,
		approval_state, requested_capabilities, approved_capabilities, last_seen_at, created_at, updated_at FROM devices`
	var args []any
	if ownerID != "" {
		query += ` WHERE owner_user_id = ?`
		args = append(args, ownerID)
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []*Device
	for rows.Next() {
		d := &Device{}
		var owner sql.NullString
		var requested, approved string
		if err := rows.Scan(&d.ID, &owner, &d.Name, &d.Fingerprint, &d.InstallationID, &d.Platform, &d.Arch,
			&d.ClientVersion, &d.ApprovalState, &requested, &approved, &d.LastSeenAt, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		d.OwnerUserID = owner.String
		if d.RequestedCapabilities, err = decodeCapabilities(requested); err != nil {
			return nil, err
		}
		if d.ApprovedCapabilities, err = decodeCapabilities(approved); err != nil {
			return nil, err
		}
		d.DeviceMode = modeForCapabilities(d.ApprovedCapabilities)
		d.Enabled = d.ApprovalState == "approved"
		res = append(res, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (db *DB) UpdateDeviceLastSeen(deviceID string) error {
	now := time.Now()
	_, err := db.Exec(`UPDATE devices SET last_seen_at = ?, updated_at = ? WHERE id = ?`, now, now, deviceID)
	return err
}

func (db *DB) AuthorizeClientExit(clientDeviceID, exitDeviceID string) (bool, error) {
	if clientDeviceID == "" || exitDeviceID == "" {
		return false, nil
	}
	var clientOwner, exitOwner sql.NullString
	err := db.QueryRow(`SELECT owner_user_id FROM devices WHERE id = ? AND approval_state = 'approved'`, clientDeviceID).Scan(&clientOwner)
	if err != nil {
		return false, err
	}
	err = db.QueryRow(`SELECT owner_user_id FROM devices WHERE id = ? AND approval_state = 'approved'`, exitDeviceID).Scan(&exitOwner)
	if err != nil {
		return false, err
	}
	if clientOwner.Valid && exitOwner.Valid && clientOwner.String != "" && clientOwner.String == exitOwner.String {
		return true, nil
	}
	return false, nil
}

type ConnectionAudit struct {
	ID             string
	UserID         string
	ClientDeviceID string
	ExitDeviceID   string
	Protocol       string
	TargetHost     string
	TargetPort     int
	ResolvedIP     string
	StartedAt      time.Time
	EndedAt        time.Time
	BytesUp        int64
	BytesDown      int64
	Result         string
	ErrorCode      string
}

func (db *DB) InsertConnectionAudit(a *ConnectionAudit) error {
	if a.ID == "" {
		a.ID = "aud_" + uuid.New().String()[:12]
	}
	_, err := db.Exec(`INSERT INTO connection_audit (id, user_id, client_device_id, exit_device_id, protocol, target_host, target_port, resolved_ip, started_at, ended_at, bytes_up, bytes_down, result, error_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.ClientDeviceID, a.ExitDeviceID, a.Protocol, a.TargetHost, a.TargetPort, a.ResolvedIP, a.StartedAt, a.EndedAt, a.BytesUp, a.BytesDown, a.Result, a.ErrorCode)
	return err
}

// GetDeviceOwnerUserID returns the owner_user_id for a device, or empty if unknown.
func (db *DB) GetDeviceOwnerUserID(deviceID string) (string, error) {
	var owner sql.NullString
	err := db.QueryRow(`SELECT owner_user_id FROM devices WHERE id = ?`, deviceID).Scan(&owner)
	if err != nil {
		return "", err
	}
	if owner.Valid {
		return owner.String, nil
	}
	return "", nil
}

// SumTodayTraffic aggregates bytes from connection_audit for the local calendar day.
func (db *DB) SumTodayTraffic() (upload, download int64, err error) {
	return db.SumTodayTrafficForOwner("")
}

func (db *DB) SumTodayTrafficForOwner(ownerID string) (upload, download int64, err error) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	query := `SELECT COALESCE(SUM(bytes_up), 0), COALESCE(SUM(bytes_down), 0)
		FROM connection_audit WHERE started_at >= ?`
	args := []any{start}
	if ownerID != "" {
		query += ` AND user_id = ?`
		args = append(args, ownerID)
	}
	err = db.QueryRow(query, args...).Scan(&upload, &download)
	return
}

func (db *DB) SetDeviceEnabled(deviceID string, enabled bool) error {
	state := "revoked"
	if enabled {
		state = "approved"
	}
	res, err := db.Exec(`UPDATE devices SET approval_state = ?, updated_at = ? WHERE id = ?`, state, time.Now(), deviceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("device not found")
	}
	return nil
}

func (db *DB) DeleteDevice(deviceID string) error {
	res, err := db.Exec(`DELETE FROM devices WHERE id = ?`, deviceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("device not found")
	}
	return nil
}

func (db *DB) ServerInstanceID() (string, error) {
	var id string
	err := db.QueryRow(`SELECT server_instance_id FROM schema_meta WHERE id = 1`).Scan(&id)
	return id, err
}

func encodeCapabilities(capabilities []string) (string, error) {
	data, err := json.Marshal(normalizeCapabilities(capabilities))
	return string(data), err
}

func decodeCapabilities(raw string) ([]string, error) {
	var capabilities []string
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return normalizeCapabilities(capabilities), nil
}

func normalizeCapabilities(capabilities []string) []string {
	seen := make(map[string]struct{}, len(capabilities))
	out := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			continue
		}
		if _, exists := seen[capability]; exists {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out
}

func capabilitiesForMode(mode string) []string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "EXIT":
		return []string{"proxy.exit"}
	case "BOTH":
		return []string{"proxy.client", "proxy.exit"}
	default:
		return []string{"proxy.client"}
	}
}

func modeForCapabilities(capabilities []string) string {
	client, exit := false, false
	for _, capability := range capabilities {
		switch capability {
		case "proxy.client":
			client = true
		case "proxy.exit":
			exit = true
		}
	}
	if client && exit {
		return "BOTH"
	}
	if exit {
		return "EXIT"
	}
	return "CLIENT"
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
