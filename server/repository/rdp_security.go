package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RDPSecurityLog records the public TCP connection or UDP association. A
// successful tunnel opening is not evidence that Windows login succeeded.
type RDPSecurityLog struct {
	ID             string    `json:"id"`
	IngressID      string    `json:"ingressId"`
	TargetDeviceID string    `json:"targetDeviceId"`
	TargetName     string    `json:"targetName"`
	SourceIP       string    `json:"sourceIp"`
	Transport      string    `json:"transport"`
	Result         string    `json:"result"`
	Reason         string    `json:"reason"`
	StartedAt      time.Time `json:"startedAt"`
	EndedAt        time.Time `json:"endedAt"`
	BytesUp        int64     `json:"bytesUp"`
	BytesDown      int64     `json:"bytesDown"`
}

type RDPSecurityBan struct {
	ID        string     `json:"id"`
	CIDR      string     `json:"cidr"`
	IngressID string     `json:"ingressId"`
	Kind      string     `json:"kind"` // manual, auto, allow
	Reason    string     `json:"reason"`
	Actor     string     `json:"actor"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

type RDPSecurityRule struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Threshold     int    `json:"threshold"`
	WindowSeconds int    `json:"windowSeconds"`
	BanSeconds    int    `json:"banSeconds"`
}

func (db *DB) ensureRDPSecuritySchema() error {
	statements := []string{
		"CREATE TABLE IF NOT EXISTS rdp_security_logs (id TEXT PRIMARY KEY, ingress_id TEXT NOT NULL, target_device_id TEXT NOT NULL, source_ip TEXT NOT NULL, transport TEXT NOT NULL, result TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP NOT NULL, bytes_up BIGINT NOT NULL DEFAULT 0, bytes_down BIGINT NOT NULL DEFAULT 0)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_logs_started ON rdp_security_logs(started_at)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_logs_source ON rdp_security_logs(source_ip, started_at)",
		"CREATE TABLE IF NOT EXISTS rdp_security_bans (id TEXT PRIMARY KEY, cidr TEXT NOT NULL, ingress_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP, revoked_at TIMESTAMP)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_bans_active ON rdp_security_bans(revoked_at, expires_at)",
		"CREATE TABLE IF NOT EXISTS rdp_security_rules (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled BOOLEAN NOT NULL, threshold INTEGER NOT NULL, window_seconds INTEGER NOT NULL, ban_seconds INTEGER NOT NULL)",
		"INSERT OR IGNORE INTO rdp_security_rules (id,name,enabled,threshold,window_seconds,ban_seconds) VALUES ('high_frequency','高频连接防护',1,30,60,900)",
		"INSERT OR IGNORE INTO rdp_security_rules (id,name,enabled,threshold,window_seconds,ban_seconds) VALUES ('persistent_scan','持续扫描防护',1,100,300,3600)",
		"DELETE FROM rdp_security_rules WHERE id = 'login_failure'",
		"DROP TABLE IF EXISTS rdp_auth_failures",
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("RDP security schema: %w", err)
		}
	}
	return nil
}

func (db *DB) InsertRDPSecurityLog(item RDPSecurityLog) error {
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.StartedAt.IsZero() {
		item.StartedAt = time.Now().UTC()
	}
	if item.EndedAt.IsZero() {
		item.EndedAt = item.StartedAt
	}
	_, err := db.Exec("INSERT INTO rdp_security_logs (id,ingress_id,target_device_id,source_ip,transport,result,reason,started_at,ended_at,bytes_up,bytes_down) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET result=excluded.result,reason=excluded.reason,ended_at=excluded.ended_at,bytes_up=excluded.bytes_up,bytes_down=excluded.bytes_down",
		item.ID, item.IngressID, item.TargetDeviceID, item.SourceIP, item.Transport, item.Result, item.Reason, item.StartedAt.UTC(), item.EndedAt.UTC(), item.BytesUp, item.BytesDown)
	return err
}

func (db *DB) ListRDPSecurityLogs(ip, ingressID string, limit int) ([]RDPSecurityLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := "SELECT id,ingress_id,target_device_id,source_ip,transport,result,reason,started_at,ended_at,bytes_up,bytes_down FROM rdp_security_logs WHERE 1=1"
	args := make([]any, 0, 3)
	if ip != "" {
		query += " AND source_ip = ?"
		args = append(args, ip)
	}
	if ingressID != "" {
		query += " AND ingress_id = ?"
		args = append(args, ingressID)
	}
	query += " ORDER BY started_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityLog, 0)
	for rows.Next() {
		var item RDPSecurityLog
		if err := rows.Scan(&item.ID, &item.IngressID, &item.TargetDeviceID, &item.SourceIP, &item.Transport, &item.Result, &item.Reason, &item.StartedAt, &item.EndedAt, &item.BytesUp, &item.BytesDown); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func NormalizeRDPIPCIDR(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if ip, err := netip.ParseAddr(raw); err == nil {
		ip = ip.Unmap()
		return netip.PrefixFrom(ip, ip.BitLen()).String(), nil
	}
	cidr, err := netip.ParsePrefix(raw)
	if err != nil {
		return "", errors.New("invalid IP or CIDR")
	}
	if cidr.Addr().Is4In6() {
		return "", errors.New("IPv4-mapped IPv6 CIDR is not supported")
	}
	return cidr.Masked().String(), nil
}

// ListActiveRDPSecurityBans returns manual bans, automatic bans and allowlist
// entries. Revoked or expired rows remain in SQLite as historical evidence.
func (db *DB) ListActiveRDPSecurityBans() ([]RDPSecurityBan, error) {
	rows, err := db.Query("SELECT id,cidr,ingress_id,kind,reason,actor,created_at,expires_at FROM rdp_security_bans WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?) ORDER BY created_at DESC", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityBan, 0)
	for rows.Next() {
		var entry RDPSecurityBan
		var expiry sql.NullTime
		if err := rows.Scan(&entry.ID, &entry.CIDR, &entry.IngressID, &entry.Kind, &entry.Reason, &entry.Actor, &entry.CreatedAt, &expiry); err != nil {
			return nil, err
		}
		if expiry.Valid {
			t := expiry.Time
			entry.ExpiresAt = &t
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (db *DB) CreateRDPSecurityBan(cidr, ingressID, kind, reason, actor string, expiresAt *time.Time) (*RDPSecurityBan, error) {
	normalized, err := NormalizeRDPIPCIDR(cidr)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "manual", "auto", "allow":
	default:
		return nil, errors.New("invalid RDP security entry kind")
	}
	if len(reason) > 512 || len(actor) > 128 {
		return nil, errors.New("reason or actor is too long")
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return nil, errors.New("expiry must be in the future")
	}
	if ingressID != "" && kind != "auto" {
		if _, err := db.GetRDPIngress(ingressID); err != nil {
			return nil, errors.New("RDP ingress not found")
		}
	}
	entry := &RDPSecurityBan{ID: uuid.NewString(), CIDR: normalized, IngressID: ingressID, Kind: kind, Reason: reason, Actor: actor, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt}
	_, err = db.Exec("INSERT INTO rdp_security_bans (id,cidr,ingress_id,kind,reason,actor,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?)",
		entry.ID, entry.CIDR, entry.IngressID, entry.Kind, entry.Reason, entry.Actor, entry.CreatedAt, entry.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (db *DB) RevokeRDPSecurityBan(id, actor string) error {
	if id == "" || actor == "" {
		return errors.New("ban ID and operator required")
	}
	result, err := db.Exec("UPDATE rdp_security_bans SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", time.Now().UTC(), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	// Keep the deletion actor alongside the historical row for auditing.
	_, err = db.Exec("INSERT INTO authorization_audit (id,action,actor_user_id,target_type,target_id,details,created_at) VALUES (?,?,?,?,?,?,?)",
		uuid.NewString(), "rdp.security.revoke", actor, "rdp_security_ban", id, "{}", time.Now().UTC())
	return err
}

func (db *DB) ListRDPSecurityRules() ([]RDPSecurityRule, error) {
	rows, err := db.Query("SELECT id,name,enabled,threshold,window_seconds,ban_seconds FROM rdp_security_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityRule, 0)
	for rows.Next() {
		var item RDPSecurityRule
		if err := rows.Scan(&item.ID, &item.Name, &item.Enabled, &item.Threshold, &item.WindowSeconds, &item.BanSeconds); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) UpdateRDPSecurityRule(rule RDPSecurityRule) error {
	if rule.Threshold < 1 || rule.Threshold > 100000 || rule.WindowSeconds < 1 || rule.WindowSeconds > 86400 || rule.BanSeconds < 60 || rule.BanSeconds > 31536000 {
		return errors.New("invalid rule threshold, window or ban duration")
	}
	result, err := db.Exec("UPDATE rdp_security_rules SET enabled=?,threshold=?,window_seconds=?,ban_seconds=? WHERE id=?",
		rule.Enabled, rule.Threshold, rule.WindowSeconds, rule.BanSeconds, rule.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) PruneRDPSecurityLogs(cutoff time.Time) error {
	_, err := db.Exec("DELETE FROM rdp_security_logs WHERE ended_at < ?", cutoff.UTC())
	return err
}
