package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

type RDPIngress struct {
	ID              string     `json:"id"`
	ServiceID       string     `json:"serviceId"`
	TargetDeviceID  string     `json:"targetDeviceId"`
	TargetName      string     `json:"targetName"`
	ListenPort      int        `json:"listenPort"`
	Status          string     `json:"status"`
	SourceCIDRs     []string   `json:"sourceCidrs"`
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	RateLimitPerMin int        `json:"rateLimitPerMinute"`
	CreatedBy       string     `json:"createdBy,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (db *DB) ListRDPIngress(ownerID string) ([]*RDPIngress, error) {
	query := `SELECT allocation.id, allocation.service_id, service.device_id, device.name,
		allocation.listen_port, allocation.status, allocation.source_cidrs, allocation.expires_at,
		allocation.rate_limit_per_min, allocation.created_by, allocation.created_at, allocation.updated_at
		FROM rdp_port_allocations allocation
		JOIN rdp_services service ON service.id = allocation.service_id
		JOIN devices device ON device.id = service.device_id`
	args := []any{}
	if ownerID != "" {
		query += ` WHERE device.owner_user_id = ?`
		args = append(args, ownerID)
	}
	query += ` ORDER BY allocation.listen_port`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*RDPIngress, 0)
	for rows.Next() {
		item := &RDPIngress{}
		var rawCIDRs string
		var expires sql.NullTime
		if err := rows.Scan(&item.ID, &item.ServiceID, &item.TargetDeviceID, &item.TargetName,
			&item.ListenPort, &item.Status, &rawCIDRs, &expires, &item.RateLimitPerMin,
			&item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rawCIDRs), &item.SourceCIDRs); err != nil {
			return nil, err
		}
		if expires.Valid {
			value := expires.Time
			item.ExpiresAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (db *DB) CreateRDPIngress(targetDeviceID, actor string, requestedPort int, sourceCIDRs []string, expiresAt *time.Time, rateLimit int, portStart, portEnd int) (*RDPIngress, error) {
	if targetDeviceID == "" || actor == "" {
		return nil, errors.New("RDP ingress target and actor are required")
	}
	if rateLimit <= 0 {
		rateLimit = 120
	}
	if portStart < 1 || portEnd < portStart || portEnd > 65535 {
		return nil, errors.New("invalid RDP ingress port range")
	}
	if len(sourceCIDRs) == 0 {
		sourceCIDRs = []string{"0.0.0.0/0", "::/0"}
	}
	normalizedCIDRs := make([]string, 0, len(sourceCIDRs))
	for _, raw := range sourceCIDRs {
		if value := strings.TrimSpace(raw); value != "" {
			normalizedCIDRs = append(normalizedCIDRs, value)
		}
	}
	if len(normalizedCIDRs) == 0 {
		normalizedCIDRs = []string{"0.0.0.0/0", "::/0"}
	}
	sourceCIDRs = slices.Clone(normalizedCIDRs)
	rawCIDRs, err := json.Marshal(sourceCIDRs)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var serviceID, name string
	var publicCaps string
	if err := tx.QueryRow(`SELECT service.id, device.name, device.approved_capabilities
		FROM rdp_services service JOIN devices device ON device.id = service.device_id
		WHERE device.id = ? AND device.approval_state = 'approved' AND service.enabled = TRUE`, targetDeviceID).
		Scan(&serviceID, &name, &publicCaps); err != nil {
		return nil, err
	}
	if !hasCapabilityJSON(publicCaps, "rdp.host") || !hasCapabilityJSON(publicCaps, "rdp.public") {
		return nil, errors.New("target device is not approved for public RDP ingress")
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM rdp_port_allocations WHERE service_id = ?`, serviceID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, errors.New("RDP target already has an ingress allocation")
	}
	port := requestedPort
	if port == 0 {
		for port = portStart; port <= portEnd; port++ {
			var count int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM rdp_port_allocations WHERE listen_port = ?`, port).Scan(&count); err != nil {
				return nil, err
			}
			if count == 0 {
				break
			}
		}
		if port > portEnd {
			return nil, errors.New("no free RDP ingress port")
		}
	} else if port < 1 || port > 65535 {
		return nil, errors.New("invalid RDP ingress port")
	}
	now := time.Now().UTC()
	id := "rdping_" + uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO rdp_port_allocations
		(id, service_id, listen_port, status, source_cidrs, expires_at, rate_limit_per_min, created_by, created_at, updated_at)
		VALUES (?, ?, ?, 'enabled', ?, ?, ?, ?, ?, ?)`, id, serviceID, port, string(rawCIDRs), expiresAt, rateLimit, actor, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &RDPIngress{ID: id, ServiceID: serviceID, TargetDeviceID: targetDeviceID, TargetName: name,
		ListenPort: port, Status: "enabled", SourceCIDRs: sourceCIDRs, ExpiresAt: expiresAt,
		RateLimitPerMin: rateLimit, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}, nil
}

func (db *DB) SetRDPIngressStatus(id, ownerID string, enabled bool) error {
	if id == "" {
		return errors.New("RDP ingress id is required")
	}
	status := "disabled"
	if enabled {
		status = "enabled"
	}
	query := `UPDATE rdp_port_allocations SET status = ?, updated_at = ? WHERE id = ?`
	args := []any{status, time.Now().UTC(), id}
	if ownerID != "" {
		query = `UPDATE rdp_port_allocations SET status = ?, updated_at = ? WHERE id = ? AND service_id IN
			(SELECT service.id FROM rdp_services service JOIN devices device ON device.id = service.device_id WHERE device.owner_user_id = ?)`
		args = append(args, ownerID)
	}
	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (db *DB) DeleteRDPIngress(id, ownerID string) error {
	query := `DELETE FROM rdp_port_allocations WHERE id = ?`
	args := []any{id}
	if ownerID != "" {
		query = `DELETE FROM rdp_port_allocations WHERE id = ? AND service_id IN
			(SELECT service.id FROM rdp_services service JOIN devices device ON device.id = service.device_id WHERE device.owner_user_id = ?)`
		args = append(args, ownerID)
	}
	result, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return sql.ErrNoRows
	}
	return err
}

func (db *DB) GetRDPIngress(id string) (*RDPIngress, error) {
	items, err := db.ListRDPIngress("")
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (db *DB) IsRDPIngressAllowed(id, source string) (*RDPIngress, error) {
	item, err := db.GetRDPIngress(id)
	if err != nil {
		return nil, err
	}
	if item.Status != "enabled" || (item.ExpiresAt != nil && !time.Now().Before(*item.ExpiresAt)) {
		return nil, errors.New("RDP ingress is disabled or expired")
	}
	_ = source // CIDR evaluation is kept in server/rdp where parsed networks are cached.
	return item, nil
}
