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
	if err := wrapper.ensureMessageSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("message schema migration failed: %w", err)
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

func (db *DB) ensureMessageSchema() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id VARCHAR(64) PRIMARY KEY,
			channel_id VARCHAR(80),
			title VARCHAR(200) NOT NULL,
			content TEXT NOT NULL,
			verification_code VARCHAR(64),
			route_rule VARCHAR(120),
			source VARCHAR(120),
			created_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_deliveries (
			message_id VARCHAR(64) NOT NULL,
			device_id VARCHAR(36) NOT NULL,
			device_name VARCHAR(100) NOT NULL,
			status VARCHAR(20) NOT NULL,
			error TEXT,
			delivered_at TIMESTAMP,
			PRIMARY KEY(message_id, device_id),
			FOREIGN KEY(message_id) REFERENCES messages(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS message_channels (
			id VARCHAR(80) PRIMARY KEY,
			name VARCHAR(120) NOT NULL,
			all_devices BOOLEAN NOT NULL DEFAULT FALSE,
			use_default_verification BOOLEAN NOT NULL DEFAULT TRUE,
			verification_rules TEXT NOT NULL DEFAULT '[]',
			route_rules TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_channel_devices (
			channel_id VARCHAR(80) NOT NULL,
			device_id VARCHAR(36) NOT NULL,
			PRIMARY KEY(channel_id, device_id),
			FOREIGN KEY(channel_id) REFERENCES message_channels(id) ON DELETE CASCADE,
			FOREIGN KEY(device_id) REFERENCES devices(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_message_deliveries_device ON message_deliveries(device_id, status)`,
		`CREATE INDEX IF NOT EXISTS idx_message_channel_devices_device ON message_channel_devices(device_id)`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	if err := db.ensureSQLiteColumn("messages", "channel_id", "VARCHAR(80)"); err != nil {
		return err
	}
	if err := db.ensureSQLiteColumn("messages", "route_rule", "VARCHAR(120)"); err != nil {
		return err
	}
	if err := db.ensureSQLiteColumn("message_channels", "use_default_verification", "BOOLEAN NOT NULL DEFAULT TRUE"); err != nil {
		return err
	}
	if err := db.ensureSQLiteColumn("message_channels", "verification_rules", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := db.ensureSQLiteColumn("message_channels", "route_rules", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	return nil
}

func (db *DB) ensureSQLiteColumn(table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var name, kind string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

type VerificationRule struct {
	Name          string   `json:"name,omitempty"`
	Keywords      []string `json:"keywords,omitempty"`
	Pattern       string   `json:"pattern,omitempty"`
	MaxDistance   int      `json:"maxDistance,omitempty"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
}

type MessageRouteRule struct {
	Name          string   `json:"name"`
	MatchType     string   `json:"matchType"`
	Pattern       string   `json:"pattern"`
	CaseSensitive bool     `json:"caseSensitive,omitempty"`
	AllDevices    bool     `json:"allDevices"`
	DeviceIDs     []string `json:"deviceIds,omitempty"`
}

type MessageChannel struct {
	ID                     string             `json:"id"`
	Name                   string             `json:"name"`
	AllDevices             bool               `json:"allDevices"`
	DeviceIDs              []string           `json:"deviceIds"`
	UseDefaultVerification bool               `json:"useDefaultVerification"`
	VerificationRules      []VerificationRule `json:"verificationRules,omitempty"`
	RouteRules             []MessageRouteRule `json:"routeRules,omitempty"`
	CreatedAt              time.Time          `json:"createdAt"`
	UpdatedAt              time.Time          `json:"updatedAt"`
}

func (db *DB) CreateMessageChannel(channel *MessageChannel) error {
	if channel == nil {
		return errors.New("channel is required")
	}
	if channel.ID == "" {
		channel.ID = "ch_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	}
	if !channel.UseDefaultVerification && len(channel.VerificationRules) == 0 {
		channel.UseDefaultVerification = true
	}
	now := time.Now().UTC()
	channel.CreatedAt = now
	channel.UpdatedAt = now
	return db.saveMessageChannel(channel, true)
}

func (db *DB) UpdateMessageChannel(channel *MessageChannel) error {
	if channel == nil || channel.ID == "" {
		return errors.New("channel id is required")
	}
	channel.UpdatedAt = time.Now().UTC()
	return db.saveMessageChannel(channel, false)
}

func (db *DB) saveMessageChannel(channel *MessageChannel, create bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	verificationRules, err := json.Marshal(channel.VerificationRules)
	if err != nil {
		return err
	}
	routeRules, err := json.Marshal(channel.RouteRules)
	if err != nil {
		return err
	}

	if create {
		if _, err := tx.Exec(`INSERT INTO message_channels
			(id, name, all_devices, use_default_verification, verification_rules, route_rules, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			channel.ID, channel.Name, channel.AllDevices, channel.UseDefaultVerification,
			string(verificationRules), string(routeRules), channel.CreatedAt, channel.UpdatedAt); err != nil {
			return err
		}
	} else {
		result, err := tx.Exec(`UPDATE message_channels
			SET name = ?, all_devices = ?, use_default_verification = ?, verification_rules = ?, route_rules = ?, updated_at = ?
			WHERE id = ?`,
			channel.Name, channel.AllDevices, channel.UseDefaultVerification, string(verificationRules), string(routeRules), channel.UpdatedAt, channel.ID)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil {
			return err
		} else if rows != 1 {
			return sql.ErrNoRows
		}
		if _, err := tx.Exec(`DELETE FROM message_channel_devices WHERE channel_id = ?`, channel.ID); err != nil {
			return err
		}
	}
	if !channel.AllDevices {
		seen := make(map[string]bool, len(channel.DeviceIDs))
		for _, deviceID := range channel.DeviceIDs {
			deviceID = strings.TrimSpace(deviceID)
			if deviceID == "" || seen[deviceID] {
				continue
			}
			seen[deviceID] = true
			if _, err := tx.Exec(`INSERT INTO message_channel_devices (channel_id, device_id) VALUES (?, ?)`, channel.ID, deviceID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (db *DB) DeleteMessageChannel(id string) error {
	result, err := db.Exec(`DELETE FROM message_channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) GetMessageChannel(id string) (*MessageChannel, error) {
	channel := &MessageChannel{}
	var verificationRules, routeRules string
	if err := db.QueryRow(`SELECT id, name, all_devices, use_default_verification,
		COALESCE(verification_rules, '[]'), COALESCE(route_rules, '[]'), created_at, updated_at
		FROM message_channels WHERE id = ?`, id).Scan(
		&channel.ID, &channel.Name, &channel.AllDevices, &channel.UseDefaultVerification,
		&verificationRules, &routeRules, &channel.CreatedAt, &channel.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(verificationRules), &channel.VerificationRules); err != nil {
		return nil, fmt.Errorf("decode verification rules for channel %s: %w", id, err)
	}
	if err := json.Unmarshal([]byte(routeRules), &channel.RouteRules); err != nil {
		return nil, fmt.Errorf("decode route rules for channel %s: %w", id, err)
	}
	if !channel.AllDevices {
		rows, err := db.Query(`SELECT device_id FROM message_channel_devices WHERE channel_id = ? ORDER BY device_id`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var deviceID string
			if err := rows.Scan(&deviceID); err != nil {
				rows.Close()
				return nil, err
			}
			channel.DeviceIDs = append(channel.DeviceIDs, deviceID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return channel, nil
}

func (db *DB) ListMessageChannels() ([]*MessageChannel, error) {
	rows, err := db.Query(`SELECT id FROM message_channels ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	channels := make([]*MessageChannel, 0, len(ids))
	for _, id := range ids {
		channel, err := db.GetMessageChannel(id)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

func (db *DB) ResolveMessageChannelTargets(id string) (*MessageChannel, []*Device, error) {
	channel, err := db.GetMessageChannel(id)
	if err != nil {
		return nil, nil, err
	}
	targets, err := db.ResolveMessageTargets(channel.AllDevices, channel.DeviceIDs)
	return channel, targets, err
}

func (db *DB) ResolveMessageTargets(allDevices bool, deviceIDs []string) ([]*Device, error) {
	devices, err := db.ListDevicesForOwner("")
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		selected[deviceID] = true
	}
	targets := make([]*Device, 0, len(devices))
	for _, device := range devices {
		if device.ApprovalState != "approved" {
			continue
		}
		if allDevices || selected[device.ID] {
			targets = append(targets, device)
		}
	}
	return targets, nil
}

type MessageDelivery struct {
	DeviceID    string     `json:"deviceId"`
	DeviceName  string     `json:"deviceName"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	DeliveredAt *time.Time `json:"deliveredAt,omitempty"`
}

type MessageRecord struct {
	ID               string            `json:"id"`
	ChannelID        string            `json:"channelId,omitempty"`
	Title            string            `json:"title"`
	Content          string            `json:"content"`
	VerificationCode string            `json:"verificationCode,omitempty"`
	RouteRule        string            `json:"routeRule,omitempty"`
	Source           string            `json:"source,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
	Deliveries       []MessageDelivery `json:"deliveries"`
}

func (db *DB) CreateMessage(message *MessageRecord, targets []*Device) error {
	if message == nil {
		return errors.New("message is required")
	}
	if message.ID == "" {
		message.ID = "msg_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO messages (id, channel_id, title, content, verification_code, route_rule, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, message.ID, nullableString(message.ChannelID), message.Title, message.Content,
		nullableString(message.VerificationCode), nullableString(message.RouteRule), nullableString(message.Source), message.CreatedAt); err != nil {
		return err
	}
	message.Deliveries = make([]MessageDelivery, 0, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO message_deliveries (message_id, device_id, device_name, status, error, delivered_at)
			VALUES (?, ?, ?, ?, NULL, NULL)`, message.ID, target.ID, target.Name, "pending"); err != nil {
			return err
		}
		message.Deliveries = append(message.Deliveries, MessageDelivery{DeviceID: target.ID, DeviceName: target.Name, Status: "pending"})
	}
	return tx.Commit()
}

func (db *DB) UpdateMessageDelivery(messageID, deviceID, status, errorMessage string, deliveredAt *time.Time) error {
	var delivered any
	if deliveredAt != nil {
		delivered = deliveredAt.UTC()
	}
	_, err := db.Exec(`UPDATE message_deliveries
		SET status = ?, error = ?, delivered_at = ?
		WHERE message_id = ? AND device_id = ?`,
		status, nullableString(errorMessage), delivered, messageID, deviceID)
	return err
}

func (db *DB) ListMessages(ownerID string, limit int) ([]*MessageRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `SELECT m.id, COALESCE(m.channel_id, ''), m.title, m.content, COALESCE(m.verification_code, ''),
		COALESCE(m.route_rule, ''), COALESCE(m.source, ''), m.created_at
		FROM messages m`
	args := make([]any, 0, 2)
	if ownerID != "" {
		query += ` WHERE EXISTS (
			SELECT 1 FROM message_deliveries md
			JOIN devices d ON d.id = md.device_id
			WHERE md.message_id = m.id AND d.owner_user_id = ?
		)`
		args = append(args, ownerID)
	}
	query += ` ORDER BY m.created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	messages := make([]*MessageRecord, 0, limit)
	for rows.Next() {
		message := &MessageRecord{}
		if err := rows.Scan(&message.ID, &message.ChannelID, &message.Title, &message.Content,
			&message.VerificationCode, &message.RouteRule, &message.Source, &message.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, message := range messages {
		deliveryRows, err := db.Query(`SELECT device_id, device_name, status, COALESCE(error, ''), delivered_at
			FROM message_deliveries WHERE message_id = ? ORDER BY device_name, device_id`, message.ID)
		if err != nil {
			return nil, err
		}
		for deliveryRows.Next() {
			var delivery MessageDelivery
			var delivered sql.NullTime
			if err := deliveryRows.Scan(&delivery.DeviceID, &delivery.DeviceName, &delivery.Status, &delivery.Error, &delivered); err != nil {
				deliveryRows.Close()
				return nil, err
			}
			if delivered.Valid {
				value := delivered.Time
				delivery.DeliveredAt = &value
			}
			message.Deliveries = append(message.Deliveries, delivery)
		}
		if err := deliveryRows.Err(); err != nil {
			deliveryRows.Close()
			return nil, err
		}
		deliveryRows.Close()
	}
	return messages, nil
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

// InsertConnectionAudits amortizes SQLite transaction/fsync overhead across a
// burst of completed proxy connections. The caller retains ownership of the
// audit objects and may reuse its slice after this method returns.
func (db *DB) InsertConnectionAudits(audits []*ConnectionAudit) error {
	if len(audits) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO connection_audit (id, user_id, client_device_id, exit_device_id, protocol, target_host, target_port, resolved_ip, started_at, ended_at, bytes_up, bytes_down, result, error_code)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, a := range audits {
		if a == nil {
			continue
		}
		if a.ID == "" {
			a.ID = "aud_" + uuid.New().String()[:12]
		}
		if _, err := stmt.Exec(
			a.ID, a.UserID, a.ClientDeviceID, a.ExitDeviceID, a.Protocol, a.TargetHost, a.TargetPort,
			a.ResolvedIP, a.StartedAt, a.EndedAt, a.BytesUp, a.BytesDown, a.Result, a.ErrorCode,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
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

func (db *DB) DeleteDevice(deviceID, reviewerID string) error {
	if deviceID == "" || reviewerID == "" {
		return errors.New("device id and reviewer id are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var fingerprint string
	if err := tx.QueryRow(`SELECT public_key_fingerprint FROM devices WHERE id = ?`, deviceID).Scan(&fingerprint); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := insertAuthorizationAudit(tx, "device.delete", reviewerID, "device", deviceID,
		map[string]any{"fingerprint": fingerprint}, now); err != nil {
		return err
	}

	// Remove the identity and its previous enrollment record first. The identity
	// table intentionally does not cascade from devices: deleting these rows
	// makes the same installation appear as a fresh pending enrollment if it
	// connects again later.
	if _, err := tx.Exec(`DELETE FROM device_enrollment_requests WHERE fingerprint = ?`, fingerprint); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM device_identities WHERE fingerprint = ? AND device_id = ?`, fingerprint, deviceID); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM devices WHERE id = ?`, deviceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
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
