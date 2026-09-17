package repository

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// RDPTarget is the server-side view of an approved target. Its local address
// is intentionally not exposed to controllers; the target Agent owns that
// configuration and always dials its own RDP service.
type RDPTarget struct {
	DeviceID string    `json:"deviceId"`
	Name     string    `json:"name"`
	Online   bool      `json:"online"`
	Service  string    `json:"-"` // internal service lookup; never exposed to controllers
	Port     int       `json:"-"` // target-local port; never exposed to controllers
	Updated  time.Time `json:"-"`
}

func (db *DB) ListRDPTargetsForController(controllerID string) ([]*RDPTarget, error) {
	if controllerID == "" {
		return nil, errors.New("controller device id is required")
	}
	rows, err := db.Query(`SELECT target.id, target.name, target.last_seen_at, service.id, service.target_port, service.updated_at
		FROM rdp_access_grants access
		JOIN devices controller ON controller.id = access.controller_device_id
		JOIN devices target ON target.id = access.target_device_id
		JOIN rdp_services service ON service.device_id = target.id AND service.enabled = TRUE
		WHERE access.controller_device_id = ?
		  AND controller.approval_state = 'approved'
		  AND target.approval_state = 'approved'
		  AND controller.owner_user_id IS NOT NULL
		  AND controller.owner_user_id = target.owner_user_id
		ORDER BY target.name, target.id`, controllerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]*RDPTarget, 0)
	for rows.Next() {
		item := &RDPTarget{}
		var lastSeen sql.NullTime
		if err := rows.Scan(&item.DeviceID, &item.Name, &lastSeen, &item.Service, &item.Port, &item.Updated); err != nil {
			return nil, err
		}
		item.Online = lastSeen.Valid && time.Since(lastSeen.Time) <= 2*time.Minute
		result = append(result, item)
	}
	return result, rows.Err()
}

// AuthorizeRDP is the only server-side admission check for a controller to
// target connection. Both devices must be approved, have the matching product
// capabilities, belong to the same owner, and have an explicit access row.
func (db *DB) AuthorizeRDP(controllerID, targetID string) (bool, error) {
	if controllerID == "" || targetID == "" || controllerID == targetID {
		return false, nil
	}
	var controllerOwner, targetOwner sql.NullString
	var controllerCaps, targetCaps string
	var granted int
	err := db.QueryRow(`SELECT controller.owner_user_id, controller.approved_capabilities,
		target.owner_user_id, target.approved_capabilities,
		CASE WHEN EXISTS (
			SELECT 1 FROM rdp_access_grants access
			JOIN rdp_services service ON service.device_id = access.target_device_id AND service.enabled = TRUE
			WHERE access.controller_device_id = controller.id AND access.target_device_id = target.id
		) THEN 1 ELSE 0 END
		FROM devices controller
		JOIN devices target ON target.id = ?
		WHERE controller.id = ?
		  AND controller.approval_state = 'approved'
		  AND target.approval_state = 'approved'`, targetID, controllerID).
		Scan(&controllerOwner, &controllerCaps, &targetOwner, &targetCaps, &granted)
	if err != nil {
		return false, err
	}
	if !hasCapabilityJSON(controllerCaps, "rdp.controller") || !hasCapabilityJSON(targetCaps, "rdp.host") || !controllerOwner.Valid || !targetOwner.Valid || controllerOwner.String != targetOwner.String {
		return false, nil
	}
	return granted == 1, nil
}

func hasCapabilityJSON(raw, wanted string) bool {
	var caps []string
	if json.Unmarshal([]byte(raw), &caps) != nil {
		return false
	}
	for _, cap := range caps {
		if cap == wanted {
			return true
		}
	}
	return false
}
