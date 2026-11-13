package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sort"
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
	SourceCountry  string    `json:"sourceCountry"`
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
	// ClearLogs purges the source's retained connection audit when the ban is
	// created and suppresses further audit rows while the ban stays active.
	ClearLogs bool `json:"clearLogs"`
}

// RDPSecurityBanInput is the full set of options for a new policy entry.
// A nil ExpiresAt means permanent.
type RDPSecurityBanInput struct {
	CIDR      string
	IngressID string
	Kind      string
	Reason    string
	Actor     string
	ExpiresAt *time.Time
	ClearLogs bool
}

const (
	RDPRuleScopeIngress = "ingress" // count and ban per public entrance
	RDPRuleScopeGlobal  = "global"  // aggregate all entrances, ban everywhere

	RDPRuleMetricConnections = "connections" // new connection attempts
	RDPRuleMetricBans        = "bans"        // automatic bans issued by other rules

	// RDPRuleMaxWindowSeconds matches the 30-day audit retention so a rule can
	// always be re-seeded from SQLite after a restart.
	RDPRuleMaxWindowSeconds = 30 * 24 * 3600
	RDPRuleMaxBanSeconds    = 365 * 24 * 3600
)

// RDPSecurityRule is an automatic ban rule. BanSeconds == 0 bans permanently.
type RDPSecurityRule struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Threshold     int    `json:"threshold"`
	WindowSeconds int    `json:"windowSeconds"`
	BanSeconds    int    `json:"banSeconds"`
	Scope         string `json:"scope"`
	Metric        string `json:"metric"`
	CountUDP      bool   `json:"countUdp"`
	PrefixV4      int    `json:"prefixV4"`
	PrefixV6      int    `json:"prefixV6"`
	ClearLogs     bool   `json:"clearLogs"`
	Builtin       bool   `json:"builtin"`
	Description   string `json:"description"`
}

// Permanent reports whether a triggered rule creates a ban without expiry.
func (r RDPSecurityRule) Permanent() bool { return r.BanSeconds == 0 }

// Built-in presets cover the common attack shapes. Only the two historical
// rules are enabled by default; the others are conservative presets an
// administrator enables after adding their own addresses to the allowlist.
var builtinRDPSecurityRules = []RDPSecurityRule{
	{ID: "high_frequency", Name: "高频连接防护", Enabled: true, Threshold: 30, WindowSeconds: 60, BanSeconds: 900, Scope: RDPRuleScopeIngress,
		Description: "短时间突发大量 TCP 连接（暴力破解、爆破工具）。"},
	{ID: "persistent_scan", Name: "持续扫描防护", Enabled: true, Threshold: 100, WindowSeconds: 300, BanSeconds: 3600, Scope: RDPRuleScopeIngress,
		Description: "数分钟内持续不断的连接尝试。"},
	{ID: "low_frequency_scan", Name: "低频慢速扫描", Enabled: false, Threshold: 30, WindowSeconds: 86400, BanSeconds: 604800, Scope: RDPRuleScopeGlobal,
		Description: "几分钟一次的慢速探测：按 24 小时、全部入口合并统计，重启后从审计记录恢复计数。"},
	{ID: "subnet_scan", Name: "网段轮换扫描", Enabled: false, Threshold: 60, WindowSeconds: 3600, BanSeconds: 86400, Scope: RDPRuleScopeGlobal, PrefixV4: 24, PrefixV6: 64,
		Description: "同一 IPv4 /24 或 IPv6 /64 内轮换地址探测，触发后封禁整个网段。"},
	{ID: "udp_probe", Name: "TCP+UDP 混合探测", Enabled: false, Threshold: 120, WindowSeconds: 300, BanSeconds: 3600, Scope: RDPRuleScopeIngress, CountUDP: true,
		Description: "同时统计新的 UDP 关联，覆盖只用 UDP 探测的扫描器（UDP 源地址可被伪造，请谨慎设置阈值）。"},
	{ID: "long_term_scan", Name: "长期累计探测", Enabled: false, Threshold: 300, WindowSeconds: RDPRuleMaxWindowSeconds, BanSeconds: 0, Scope: RDPRuleScopeGlobal,
		Description: "30 天内累计连接次数过多时永久封禁。"},
	{ID: "repeat_offender", Name: "累犯升级永久封禁", Enabled: false, Threshold: 2, WindowSeconds: 7 * 86400, BanSeconds: 0, Scope: RDPRuleScopeGlobal, Metric: RDPRuleMetricBans,
		Description: "统计其他规则产生的自动封禁次数，7 天内被自动封禁超过 2 次即永久封禁。"},
}

func builtinRDPSecurityRule(id string) (RDPSecurityRule, int, bool) {
	for i, rule := range builtinRDPSecurityRules {
		if rule.ID == id {
			return rule, i, true
		}
	}
	return RDPSecurityRule{}, -1, false
}

func (db *DB) ensureRDPSecuritySchema() error {
	statements := []string{
		"CREATE TABLE IF NOT EXISTS rdp_security_logs (id TEXT PRIMARY KEY, ingress_id TEXT NOT NULL, target_device_id TEXT NOT NULL, source_ip TEXT NOT NULL, source_country TEXT NOT NULL DEFAULT '', transport TEXT NOT NULL, result TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', started_at TIMESTAMP NOT NULL, ended_at TIMESTAMP NOT NULL, bytes_up BIGINT NOT NULL DEFAULT 0, bytes_down BIGINT NOT NULL DEFAULT 0)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_logs_started ON rdp_security_logs(started_at)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_logs_source ON rdp_security_logs(source_ip, started_at)",
		"CREATE TABLE IF NOT EXISTS rdp_security_bans (id TEXT PRIMARY KEY, cidr TEXT NOT NULL, ingress_id TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP, revoked_at TIMESTAMP)",
		"CREATE INDEX IF NOT EXISTS idx_rdp_security_bans_active ON rdp_security_bans(revoked_at, expires_at)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_rdp_geo_bans_unique ON rdp_security_bans(cidr) WHERE kind='auto' AND ingress_id='' AND expires_at IS NULL AND revoked_at IS NULL AND reason LIKE 'GEO_NON_CN:%'",
		"CREATE TABLE IF NOT EXISTS rdp_security_rules (id TEXT PRIMARY KEY, name TEXT NOT NULL, enabled BOOLEAN NOT NULL, threshold INTEGER NOT NULL, window_seconds INTEGER NOT NULL, ban_seconds INTEGER NOT NULL)",
		"DELETE FROM rdp_security_rules WHERE id = 'login_failure'",
		"DROP TABLE IF EXISTS rdp_auth_failures",
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("RDP security schema: %w", err)
		}
	}
	// Existing deployments need new columns too; CREATE TABLE IF NOT EXISTS
	// does not migrate a pre-existing table.
	columns := []struct{ table, column, ddl string }{
		{"rdp_security_logs", "source_country", "TEXT NOT NULL DEFAULT ''"},
		{"rdp_security_bans", "clear_logs", "BOOLEAN NOT NULL DEFAULT 0"},
		{"rdp_security_rules", "scope", "TEXT NOT NULL DEFAULT 'ingress'"},
		{"rdp_security_rules", "metric", "TEXT NOT NULL DEFAULT 'connections'"},
		{"rdp_security_rules", "count_udp", "BOOLEAN NOT NULL DEFAULT 0"},
		{"rdp_security_rules", "prefix_v4", "INTEGER NOT NULL DEFAULT 32"},
		{"rdp_security_rules", "prefix_v6", "INTEGER NOT NULL DEFAULT 128"},
		{"rdp_security_rules", "clear_logs", "BOOLEAN NOT NULL DEFAULT 0"},
		{"rdp_security_rules", "builtin", "BOOLEAN NOT NULL DEFAULT 0"},
	}
	for _, c := range columns {
		if err := db.ensureColumn(c.table, c.column, c.ddl); err != nil {
			return fmt.Errorf("RDP security schema: %w", err)
		}
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_rdp_security_bans_created ON rdp_security_bans(kind, created_at)"); err != nil {
		return fmt.Errorf("RDP security schema: %w", err)
	}
	for _, rule := range builtinRDPSecurityRules {
		normalized := rule
		if err := normalizeRDPSecurityRule(&normalized); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO rdp_security_rules
			(id,name,enabled,threshold,window_seconds,ban_seconds,scope,metric,count_udp,prefix_v4,prefix_v6,clear_logs,builtin)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1)`,
			normalized.ID, normalized.Name, normalized.Enabled, normalized.Threshold, normalized.WindowSeconds, normalized.BanSeconds,
			normalized.Scope, normalized.Metric, normalized.CountUDP, normalized.PrefixV4, normalized.PrefixV6, normalized.ClearLogs); err != nil {
			return fmt.Errorf("RDP security schema: %w", err)
		}
		if _, err := db.Exec("UPDATE rdp_security_rules SET builtin = 1 WHERE id = ?", rule.ID); err != nil {
			return fmt.Errorf("RDP security schema: %w", err)
		}
	}
	return nil
}

func (db *DB) ensureColumn(table, column, ddl string) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl))
	return err
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
	_, err := db.Exec("INSERT INTO rdp_security_logs (id,ingress_id,target_device_id,source_ip,source_country,transport,result,reason,started_at,ended_at,bytes_up,bytes_down) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET result=excluded.result,reason=excluded.reason,ended_at=excluded.ended_at,bytes_up=excluded.bytes_up,bytes_down=excluded.bytes_down,source_country=excluded.source_country",
		item.ID, item.IngressID, item.TargetDeviceID, item.SourceIP, item.SourceCountry, item.Transport, item.Result, item.Reason, item.StartedAt.UTC(), item.EndedAt.UTC(), item.BytesUp, item.BytesDown)
	return err
}

func (db *DB) ListRDPSecurityLogs(ip, ingressID string, limit int) ([]RDPSecurityLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := "SELECT id,ingress_id,target_device_id,source_ip,source_country,transport,result,reason,started_at,ended_at,bytes_up,bytes_down FROM rdp_security_logs WHERE 1=1"
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
		if err := rows.Scan(&item.ID, &item.IngressID, &item.TargetDeviceID, &item.SourceIP, &item.SourceCountry, &item.Transport, &item.Result, &item.Reason, &item.StartedAt, &item.EndedAt, &item.BytesUp, &item.BytesDown); err != nil {
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

const rdpBanColumns = "id,cidr,ingress_id,kind,reason,actor,created_at,expires_at,clear_logs"

func scanRDPSecurityBan(scanner interface{ Scan(...any) error }) (RDPSecurityBan, error) {
	var entry RDPSecurityBan
	var expiry sql.NullTime
	if err := scanner.Scan(&entry.ID, &entry.CIDR, &entry.IngressID, &entry.Kind, &entry.Reason, &entry.Actor, &entry.CreatedAt, &expiry, &entry.ClearLogs); err != nil {
		return entry, err
	}
	if expiry.Valid {
		t := expiry.Time
		entry.ExpiresAt = &t
	}
	return entry, nil
}

// ListActiveRDPSecurityBans returns manual bans, automatic bans and allowlist
// entries. Revoked or expired rows remain in SQLite as historical evidence.
func (db *DB) ListActiveRDPSecurityBans() ([]RDPSecurityBan, error) {
	rows, err := db.Query("SELECT "+rdpBanColumns+" FROM rdp_security_bans WHERE revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?) ORDER BY created_at DESC", time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityBan, 0)
	for rows.Next() {
		entry, err := scanRDPSecurityBan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

// RDPSecurityBanFilter selects active policy entries for the admin list.
type RDPSecurityBanFilter struct {
	Search string // substring of CIDR or reason
	Kind   string // manual | auto | allow | geo | ""
	Scope  string // "" any, "global", or an ingress ID
	Expiry string // "" any, "permanent", "temporary"
	Page   int
	Size   int
}

// ListRDPSecurityBanPage paginates active policy entries server-side; GeoIP
// can produce thousands of permanent rows, so the console never loads all.
func (db *DB) ListRDPSecurityBanPage(filter RDPSecurityBanFilter) (*RDPSecurityPage[RDPSecurityBan], error) {
	if _, _, err := validateRDPLogPage(filter.Page, filter.Size); err != nil {
		return nil, err
	}
	where := []string{"revoked_at IS NULL", "(expires_at IS NULL OR expires_at > ?)"}
	args := []any{time.Now().UTC()}
	if search := strings.TrimSpace(filter.Search); search != "" {
		where = append(where, "(instr(cidr, ?) > 0 OR instr(reason, ?) > 0 OR instr(actor, ?) > 0)")
		args = append(args, search, search, search)
	}
	switch filter.Kind {
	case "":
	case "manual", "allow":
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	case "auto":
		where = append(where, "kind = 'auto' AND reason NOT LIKE 'GEO_%'")
	case "geo":
		where = append(where, "kind = 'auto' AND reason LIKE 'GEO_%'")
	default:
		return nil, errors.New("invalid kind filter")
	}
	switch scope := strings.TrimSpace(filter.Scope); scope {
	case "":
	case "global":
		where = append(where, "ingress_id = ''")
	default:
		where = append(where, "ingress_id = ?")
		args = append(args, scope)
	}
	switch filter.Expiry {
	case "":
	case "permanent":
		where = append(where, "expires_at IS NULL")
	case "temporary":
		where = append(where, "expires_at IS NOT NULL")
	default:
		return nil, errors.New("invalid expiry filter")
	}
	clause := " WHERE " + strings.Join(where, " AND ")
	output := &RDPSecurityPage[RDPSecurityBan]{Items: make([]RDPSecurityBan, 0), Page: filter.Page, PageSize: filter.Size}
	if err := db.QueryRow("SELECT COUNT(*) FROM rdp_security_bans"+clause, args...).Scan(&output.Total); err != nil {
		return nil, err
	}
	args = append(args, filter.Size, (filter.Page-1)*filter.Size)
	rows, err := db.Query("SELECT "+rdpBanColumns+" FROM rdp_security_bans"+clause+" ORDER BY created_at DESC, id ASC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		entry, err := scanRDPSecurityBan(rows)
		if err != nil {
			return nil, err
		}
		output.Items = append(output.Items, entry)
	}
	return output, rows.Err()
}

func (db *DB) CreateRDPSecurityBan(cidr, ingressID, kind, reason, actor string, expiresAt *time.Time) (*RDPSecurityBan, error) {
	return db.CreateRDPSecurityBanWithOptions(RDPSecurityBanInput{CIDR: cidr, IngressID: ingressID, Kind: kind, Reason: reason, Actor: actor, ExpiresAt: expiresAt})
}

func (db *DB) CreateRDPSecurityBanWithOptions(input RDPSecurityBanInput) (*RDPSecurityBan, error) {
	normalized, err := NormalizeRDPIPCIDR(input.CIDR)
	if err != nil {
		return nil, err
	}
	switch input.Kind {
	case "manual", "auto", "allow":
	default:
		return nil, errors.New("invalid RDP security entry kind")
	}
	if len(input.Reason) > 512 || len(input.Actor) > 128 {
		return nil, errors.New("reason or actor is too long")
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return nil, errors.New("expiry must be in the future")
	}
	if input.IngressID != "" && input.Kind != "auto" {
		if _, err := db.GetRDPIngress(input.IngressID); err != nil {
			return nil, errors.New("RDP ingress not found")
		}
	}
	// An allowlist entry never suppresses audit: it exists for trusted sources.
	clearLogs := input.ClearLogs && input.Kind != "allow"
	now := time.Now().UTC()
	if input.Kind == "manual" && input.ExpiresAt == nil {
		// Supersede temporary bans on the same address so a one-click
		// permanent ban does not leave duplicate active rows behind.
		_, _ = db.Exec(`UPDATE rdp_security_bans SET revoked_at = ?
			WHERE cidr = ? AND kind != 'allow' AND revoked_at IS NULL AND expires_at IS NOT NULL
			AND (ingress_id = ? OR ? = '')`, now, normalized, input.IngressID, input.IngressID)
	}
	entry := &RDPSecurityBan{ID: uuid.NewString(), CIDR: normalized, IngressID: input.IngressID, Kind: input.Kind, Reason: input.Reason,
		Actor: input.Actor, CreatedAt: now, ExpiresAt: input.ExpiresAt, ClearLogs: clearLogs}
	_, err = db.Exec("INSERT INTO rdp_security_bans (id,cidr,ingress_id,kind,reason,actor,created_at,expires_at,clear_logs) VALUES (?,?,?,?,?,?,?,?,?)",
		entry.ID, entry.CIDR, entry.IngressID, entry.Kind, entry.Reason, entry.Actor, entry.CreatedAt, entry.ExpiresAt, entry.ClearLogs)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// MakeRDPSecurityBanPermanent removes the expiry from an active entry. A
// temporary automatic ban becomes an operator (manual) ban so that a later
// allowlist entry cannot silently lift a decision the administrator confirmed.
func (db *DB) MakeRDPSecurityBanPermanent(id, actor string, clearLogs ...bool) (*RDPSecurityBan, error) {
	if id == "" || actor == "" {
		return nil, errors.New("ban ID and operator required")
	}
	forceClear := len(clearLogs) > 0 && clearLogs[0]
	result, err := db.Exec(`UPDATE rdp_security_bans SET expires_at = NULL,
		kind = CASE WHEN kind = 'auto' AND reason NOT LIKE 'GEO_%' THEN 'manual' ELSE kind END,
		clear_logs = CASE WHEN ? AND kind != 'allow' THEN 1 ELSE clear_logs END,
		actor = ?
		WHERE id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, forceClear, actor, id, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, sql.ErrNoRows
	}
	if _, err := db.Exec("INSERT INTO authorization_audit (id,action,actor_user_id,target_type,target_id,details,created_at) VALUES (?,?,?,?,?,?,?)",
		uuid.NewString(), "rdp.security.permanent", actor, "rdp_security_ban", id, "{}", time.Now().UTC()); err != nil {
		return nil, err
	}
	entry, err := scanRDPSecurityBan(db.QueryRow("SELECT "+rdpBanColumns+" FROM rdp_security_bans WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &entry, nil
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

// ListRDPAutoBansSince feeds the "bans" metric (repeat offender) after a
// restart. GeoIP bans are permanent already and never count.
func (db *DB) ListRDPAutoBansSince(since time.Time) ([]RDPSecurityBan, error) {
	rows, err := db.Query("SELECT "+rdpBanColumns+" FROM rdp_security_bans WHERE kind = 'auto' AND created_at >= ? AND reason NOT LIKE 'GEO_%' ORDER BY created_at ASC", since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityBan, 0)
	for rows.Next() {
		entry, err := scanRDPSecurityBan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

const rdpRuleColumns = "id,name,enabled,threshold,window_seconds,ban_seconds,scope,metric,count_udp,prefix_v4,prefix_v6,clear_logs,builtin"

func scanRDPSecurityRule(scanner interface{ Scan(...any) error }) (RDPSecurityRule, error) {
	var item RDPSecurityRule
	err := scanner.Scan(&item.ID, &item.Name, &item.Enabled, &item.Threshold, &item.WindowSeconds, &item.BanSeconds,
		&item.Scope, &item.Metric, &item.CountUDP, &item.PrefixV4, &item.PrefixV6, &item.ClearLogs, &item.Builtin)
	if err != nil {
		return item, err
	}
	if builtin, _, ok := builtinRDPSecurityRule(item.ID); ok {
		item.Description = builtin.Description
	}
	return item, nil
}

// ListRDPSecurityRules returns built-in presets first (in preset order),
// then custom rules sorted by ID.
func (db *DB) ListRDPSecurityRules() ([]RDPSecurityRule, error) {
	rows, err := db.Query("SELECT " + rdpRuleColumns + " FROM rdp_security_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RDPSecurityRule, 0)
	for rows.Next() {
		item, err := scanRDPSecurityRule(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	order := func(id string) int {
		if _, index, ok := builtinRDPSecurityRule(id); ok {
			return index
		}
		return len(builtinRDPSecurityRules)
	}
	sort.SliceStable(result, func(i, j int) bool { return order(result[i].ID) < order(result[j].ID) })
	return result, nil
}

func (db *DB) GetRDPSecurityRule(id string) (*RDPSecurityRule, error) {
	item, err := scanRDPSecurityRule(db.QueryRow("SELECT "+rdpRuleColumns+" FROM rdp_security_rules WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// normalizeRDPSecurityRule fills defaults for fields older clients omit and
// validates every bound used by the runtime counter.
func normalizeRDPSecurityRule(rule *RDPSecurityRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Scope == "" {
		rule.Scope = RDPRuleScopeIngress
	}
	if rule.Metric == "" {
		rule.Metric = RDPRuleMetricConnections
	}
	if rule.PrefixV4 == 0 {
		rule.PrefixV4 = 32
	}
	if rule.PrefixV6 == 0 {
		rule.PrefixV6 = 128
	}
	if rule.Threshold < 1 || rule.Threshold > 100000 || rule.WindowSeconds < 1 || rule.WindowSeconds > RDPRuleMaxWindowSeconds ||
		(rule.BanSeconds != 0 && (rule.BanSeconds < 60 || rule.BanSeconds > RDPRuleMaxBanSeconds)) || rule.BanSeconds < 0 {
		return errors.New("invalid rule threshold, window or ban duration")
	}
	if rule.Scope != RDPRuleScopeIngress && rule.Scope != RDPRuleScopeGlobal {
		return errors.New("rule scope must be ingress or global")
	}
	if rule.Metric != RDPRuleMetricConnections && rule.Metric != RDPRuleMetricBans {
		return errors.New("rule metric must be connections or bans")
	}
	if rule.PrefixV4 < 8 || rule.PrefixV4 > 32 || rule.PrefixV6 < 16 || rule.PrefixV6 > 128 {
		return errors.New("aggregation prefix must be IPv4 /8-/32 and IPv6 /16-/128")
	}
	if len([]rune(rule.Name)) > 64 {
		return errors.New("rule name is too long")
	}
	return nil
}

// UpdateRDPSecurityRule updates the tunable fields. An empty name keeps the
// stored name; ID and builtin status are immutable.
func (db *DB) UpdateRDPSecurityRule(rule RDPSecurityRule) error {
	if err := normalizeRDPSecurityRule(&rule); err != nil {
		return err
	}
	result, err := db.Exec(`UPDATE rdp_security_rules SET name=COALESCE(NULLIF(?,''),name),enabled=?,threshold=?,window_seconds=?,ban_seconds=?,
		scope=?,metric=?,count_udp=?,prefix_v4=?,prefix_v6=?,clear_logs=? WHERE id=?`,
		rule.Name, rule.Enabled, rule.Threshold, rule.WindowSeconds, rule.BanSeconds,
		rule.Scope, rule.Metric, rule.CountUDP, rule.PrefixV4, rule.PrefixV6, rule.ClearLogs, rule.ID)
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

// CreateRDPSecurityRule adds a custom automatic ban rule.
func (db *DB) CreateRDPSecurityRule(rule RDPSecurityRule) (*RDPSecurityRule, error) {
	if err := normalizeRDPSecurityRule(&rule); err != nil {
		return nil, err
	}
	if rule.Name == "" {
		return nil, errors.New("rule name required")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM rdp_security_rules").Scan(&count); err != nil {
		return nil, err
	}
	if count >= 64 {
		return nil, errors.New("too many RDP security rules")
	}
	rule.ID = "custom_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	rule.Builtin = false
	rule.Description = ""
	if _, err := db.Exec("INSERT INTO rdp_security_rules ("+rdpRuleColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0)",
		rule.ID, rule.Name, rule.Enabled, rule.Threshold, rule.WindowSeconds, rule.BanSeconds,
		rule.Scope, rule.Metric, rule.CountUDP, rule.PrefixV4, rule.PrefixV6, rule.ClearLogs); err != nil {
		return nil, err
	}
	return &rule, nil
}

// ErrRDPBuiltinRule is returned when deleting a preset; presets are disabled
// instead so that upgrades do not recreate them.
var ErrRDPBuiltinRule = errors.New("built-in rules cannot be deleted; disable them instead")

func (db *DB) DeleteRDPSecurityRule(id string) error {
	rule, err := db.GetRDPSecurityRule(id)
	if err != nil {
		return err
	}
	if rule.Builtin {
		return ErrRDPBuiltinRule
	}
	_, err = db.Exec("DELETE FROM rdp_security_rules WHERE id = ? AND builtin = 0", id)
	return err
}

func (db *DB) PruneRDPSecurityLogs(cutoff time.Time) error {
	_, err := db.Exec("DELETE FROM rdp_security_logs WHERE ended_at < ?", cutoff.UTC())
	return err
}
