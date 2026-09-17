package repository

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

const (
	EnrollmentPending  = "pending"
	EnrollmentApproved = "approved"
	EnrollmentRejected = "rejected"
	EnrollmentRevoked  = "revoked"
)

type DeviceIdentityObservation struct {
	Fingerprint           string
	InstallationID        string
	PublicKey             []byte
	DeviceName            string
	Platform              string
	Arch                  string
	ClientVersion         string
	RequestedCapabilities []string
}

type DeviceAuthorization struct {
	State                string
	RequestID            string
	DeviceID             string
	ApprovedCapabilities []string
	RDPTargets           []*RDPTarget
}

type EnrollmentRequest struct {
	ID                    string     `json:"id"`
	Fingerprint           string     `json:"fingerprint"`
	InstallationID        string     `json:"installationId"`
	DeviceName            string     `json:"deviceName"`
	Platform              string     `json:"platform"`
	Arch                  string     `json:"arch"`
	ClientVersion         string     `json:"clientVersion"`
	RequestedCapabilities []string   `json:"requestedCapabilities"`
	State                 string     `json:"state"`
	FirstSeenAt           time.Time  `json:"firstSeenAt"`
	LastSeenAt            time.Time  `json:"lastSeenAt"`
	ReviewedAt            *time.Time `json:"reviewedAt,omitempty"`
	ReviewedBy            string     `json:"reviewedBy,omitempty"`
	RejectionReason       string     `json:"rejectionReason,omitempty"`
}

// ObserveDeviceIdentity records an authenticated public-key identity and
// returns its server-controlled approval state. It never approves a device.
func (db *DB) ObserveDeviceIdentity(observation DeviceIdentityObservation) (*DeviceAuthorization, error) {
	if observation.Fingerprint == "" || observation.InstallationID == "" || len(observation.PublicKey) == 0 {
		return nil, errors.New("incomplete device identity")
	}
	requested, err := encodeCapabilities(observation.RequestedCapabilities)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var deviceID sql.NullString
	var installationID, status string
	var publicKey []byte
	err = tx.QueryRow(`SELECT device_id, installation_id, public_key, status FROM device_identities WHERE fingerprint = ?`, observation.Fingerprint).
		Scan(&deviceID, &installationID, &publicKey, &status)
	if err == nil {
		if installationID != observation.InstallationID || !bytes.Equal(publicKey, observation.PublicKey) {
			return nil, errors.New("device identity metadata does not match its first observation")
		}
		switch status {
		case EnrollmentApproved:
			if !deviceID.Valid || deviceID.String == "" {
				return nil, errors.New("approved identity has no device")
			}
			var state, capabilities string
			if err := tx.QueryRow(`SELECT approval_state, approved_capabilities FROM devices WHERE id = ?`, deviceID.String).
				Scan(&state, &capabilities); err != nil {
				return nil, err
			}
			approved, err := decodeCapabilities(capabilities)
			if err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &DeviceAuthorization{State: state, DeviceID: deviceID.String, ApprovedCapabilities: approved}, nil
		case EnrollmentRejected, EnrollmentRevoked:
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &DeviceAuthorization{State: status, DeviceID: deviceID.String}, nil
		case EnrollmentPending:
			// Metadata is refreshed below so the administrator sees the latest
			// name/version while one fingerprint still maps to one request.
		default:
			return nil, fmt.Errorf("unsupported identity state %q", status)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	} else {
		if _, err := tx.Exec(`INSERT INTO device_identities
			(fingerprint, device_id, installation_id, public_key, status, created_at, updated_at)
			VALUES (?, NULL, ?, ?, ?, ?, ?)`, observation.Fingerprint, observation.InstallationID,
			observation.PublicKey, EnrollmentPending, now, now); err != nil {
			return nil, err
		}
	}

	requestID := "enr_" + uuid.NewString()
	if _, err := tx.Exec(`INSERT INTO device_enrollment_requests
		(id, fingerprint, installation_id, public_key, device_name, platform, arch, client_version,
		 requested_capabilities, state, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fingerprint) DO UPDATE SET
			device_name = excluded.device_name,
			platform = excluded.platform,
			arch = excluded.arch,
			client_version = excluded.client_version,
			requested_capabilities = excluded.requested_capabilities,
			last_seen_at = excluded.last_seen_at`,
		requestID, observation.Fingerprint, observation.InstallationID, observation.PublicKey,
		fallbackDeviceName(observation.DeviceName), observation.Platform, observation.Arch,
		observation.ClientVersion, requested, EnrollmentPending, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE device_identities SET updated_at = ? WHERE fingerprint = ?`, now, observation.Fingerprint); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT id FROM device_enrollment_requests WHERE fingerprint = ?`, observation.Fingerprint).Scan(&requestID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &DeviceAuthorization{State: EnrollmentPending, RequestID: requestID}, nil
}

func (db *DB) IsDeviceIdentityApproved(fingerprint, deviceID string) bool {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM device_identities i
		JOIN devices d ON d.id = i.device_id
		WHERE i.fingerprint = ? AND i.device_id = ? AND i.status = 'approved' AND d.approval_state = 'approved'`,
		fingerprint, deviceID).Scan(&count)
	return err == nil && count == 1
}

func (db *DB) ListEnrollmentRequests(state string) ([]*EnrollmentRequest, error) {
	query := `SELECT id, fingerprint, installation_id, device_name, platform, arch, client_version,
		requested_capabilities, state, first_seen_at, last_seen_at, reviewed_at, reviewed_by, rejection_reason
		FROM device_enrollment_requests`
	var args []any
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY last_seen_at DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]*EnrollmentRequest, 0)
	for rows.Next() {
		var item EnrollmentRequest
		var capabilities string
		var reviewedAt sql.NullTime
		var reviewedBy, reason sql.NullString
		if err := rows.Scan(&item.ID, &item.Fingerprint, &item.InstallationID, &item.DeviceName,
			&item.Platform, &item.Arch, &item.ClientVersion, &capabilities, &item.State,
			&item.FirstSeenAt, &item.LastSeenAt, &reviewedAt, &reviewedBy, &reason); err != nil {
			return nil, err
		}
		item.RequestedCapabilities, err = decodeCapabilities(capabilities)
		if err != nil {
			return nil, err
		}
		if reviewedAt.Valid {
			item.ReviewedAt = &reviewedAt.Time
		}
		item.ReviewedBy, item.RejectionReason = reviewedBy.String, reason.String
		requests = append(requests, &item)
	}
	return requests, rows.Err()
}

func (db *DB) ApproveEnrollment(requestID, reviewerID string, capabilities []string) (*Device, error) {
	if requestID == "" || reviewerID == "" {
		return nil, errors.New("request id and reviewer id are required")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var request EnrollmentRequest
	var publicKey []byte
	var requestedRaw string
	err = tx.QueryRow(`SELECT fingerprint, installation_id, public_key, device_name, platform, arch,
		client_version, requested_capabilities, state FROM device_enrollment_requests WHERE id = ?`, requestID).
		Scan(&request.Fingerprint, &request.InstallationID, &publicKey, &request.DeviceName, &request.Platform,
			&request.Arch, &request.ClientVersion, &requestedRaw, &request.State)
	if err != nil {
		return nil, err
	}
	if request.State != EnrollmentPending {
		return nil, fmt.Errorf("enrollment is %s, not pending", request.State)
	}
	requested, err := decodeCapabilities(requestedRaw)
	if err != nil {
		return nil, err
	}
	approved := filterApprovedCapabilities(capabilities, requested)
	if len(approved) == 0 {
		return nil, errors.New("at least one requested capability must be approved")
	}
	if err := validateCapabilityDependencies(approved); err != nil {
		return nil, err
	}
	approvedRaw, err := encodeCapabilities(approved)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	device := &Device{
		ID: "dev_" + uuid.NewString(), OwnerUserID: reviewerID, Name: request.DeviceName,
		Fingerprint: request.Fingerprint, InstallationID: request.InstallationID,
		Platform: request.Platform, Arch: request.Arch, ClientVersion: request.ClientVersion,
		ApprovalState: EnrollmentApproved, RequestedCapabilities: requested,
		ApprovedCapabilities: approved, CreatedAt: now, UpdatedAt: now, Enabled: true,
		DeviceMode: modeForCapabilities(approved),
	}
	if _, err := tx.Exec(`INSERT INTO devices
		(id, owner_user_id, name, public_key_fingerprint, installation_id, platform, arch, client_version,
		 approval_state, requested_capabilities, approved_capabilities, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, device.ID, reviewerID, device.Name,
		device.Fingerprint, device.InstallationID, device.Platform, device.Arch, device.ClientVersion,
		EnrollmentApproved, requestedRaw, approvedRaw, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE device_identities SET device_id = ?, status = ?, updated_at = ? WHERE fingerprint = ? AND status = ?`,
		device.ID, EnrollmentApproved, now, request.Fingerprint, EnrollmentPending); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE device_enrollment_requests SET state = ?, reviewed_at = ?, reviewed_by = ? WHERE id = ? AND state = ?`,
		EnrollmentApproved, now, reviewerID, requestID, EnrollmentPending); err != nil {
		return nil, err
	}
	for _, capability := range approved {
		if _, err := tx.Exec(`INSERT INTO device_grants (device_id, capability, granted_by, granted_at) VALUES (?, ?, ?, ?)`,
			device.ID, capability, reviewerID, now); err != nil {
			return nil, err
		}
	}
	if slices.Contains(approved, "rdp.host") {
		if _, err := tx.Exec(`INSERT INTO rdp_services
			(id, device_id, name, target_host, target_port, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, TRUE, ?, ?)`,
			"rdpsvc_"+uuid.NewString(), device.ID, device.Name, "127.0.0.1", 3389, now, now); err != nil {
			return nil, err
		}
	}
	if err := syncOwnerRDPGrants(tx, reviewerID, device.ID, approved, reviewerID, now); err != nil {
		return nil, err
	}
	if err := insertAuthorizationAudit(tx, "device.approve", reviewerID, "device", device.ID,
		map[string]any{"enrollmentId": requestID, "fingerprint": request.Fingerprint, "capabilities": approved}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return device, nil
}

func (db *DB) RejectEnrollment(requestID, reviewerID, reason string) error {
	return db.reviewEnrollment(requestID, reviewerID, reason, EnrollmentRejected)
}

func (db *DB) reviewEnrollment(requestID, reviewerID, reason, state string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fingerprint string
	if err := tx.QueryRow(`SELECT fingerprint FROM device_enrollment_requests WHERE id = ? AND state = 'pending'`, requestID).Scan(&fingerprint); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(`UPDATE device_enrollment_requests SET state = ?, reviewed_at = ?, reviewed_by = ?, rejection_reason = ? WHERE id = ?`,
		state, now, reviewerID, reason, requestID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE device_identities SET status = ?, updated_at = ? WHERE fingerprint = ?`, state, now, fingerprint); err != nil {
		return err
	}
	if err := insertAuthorizationAudit(tx, "device.reject", reviewerID, "enrollment", requestID,
		map[string]any{"fingerprint": fingerprint, "reason": reason}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) RevokeDevice(deviceID, reviewerID, reason string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.Exec(`UPDATE devices SET approval_state = ?, updated_at = ? WHERE id = ? AND approval_state = ?`,
		EnrollmentRevoked, now, deviceID, EnrollmentApproved)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("approved device not found")
	}
	if _, err := tx.Exec(`UPDATE device_identities SET status = ?, updated_at = ? WHERE device_id = ?`, EnrollmentRevoked, now, deviceID); err != nil {
		return err
	}
	if err := insertAuthorizationAudit(tx, "device.revoke", reviewerID, "device", deviceID,
		map[string]any{"reason": reason}, now); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateDeviceCapabilities replaces the server-approved capabilities for an
// already approved device. A device can only receive capabilities it declared
// during its authenticated enrollment; removing every capability is rejected
// so that a fully disabled device has the explicit revoked state instead.
// Callers must invalidate the active session after the transaction commits so
// the next handshake receives the new grant set.
func (db *DB) UpdateDeviceCapabilities(deviceID, reviewerID string, capabilities []string) (*Device, error) {
	if deviceID == "" || reviewerID == "" {
		return nil, errors.New("device id and reviewer id are required")
	}
	selected := normalizeCapabilities(capabilities)
	if len(selected) == 0 {
		return nil, errors.New("at least one capability must remain approved; revoke the device to disable all access")
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var device Device
	var owner sql.NullString
	var lastSeen sql.NullTime
	var requestedRaw, approvedRaw string
	err = tx.QueryRow(`SELECT id, owner_user_id, name, public_key_fingerprint, installation_id,
		platform, arch, client_version, approval_state, requested_capabilities,
		approved_capabilities, last_seen_at, created_at, updated_at
		FROM devices WHERE id = ?`, deviceID).Scan(
		&device.ID, &owner, &device.Name, &device.Fingerprint, &device.InstallationID,
		&device.Platform, &device.Arch, &device.ClientVersion, &device.ApprovalState,
		&requestedRaw, &approvedRaw, &lastSeen, &device.CreatedAt, &device.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if device.ApprovalState != EnrollmentApproved {
		return nil, fmt.Errorf("device is %s, not approved", device.ApprovalState)
	}
	requested, err := decodeCapabilities(requestedRaw)
	if err != nil {
		return nil, err
	}
	previous, err := decodeCapabilities(approvedRaw)
	if err != nil {
		return nil, err
	}
	approved := filterApprovedCapabilities(selected, requested)
	if len(approved) == 0 {
		return nil, errors.New("selected capabilities must be requested by the device")
	}
	if err := validateCapabilityDependencies(approved); err != nil {
		return nil, err
	}
	encoded, err := encodeCapabilities(approved)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result, err := tx.Exec(`UPDATE devices SET approved_capabilities = ?, updated_at = ?
		WHERE id = ? AND approval_state = ?`, encoded, now, deviceID, EnrollmentApproved)
	if err != nil {
		return nil, err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}

	if _, err := tx.Exec(`DELETE FROM device_grants WHERE device_id = ?`, deviceID); err != nil {
		return nil, err
	}
	for _, capability := range approved {
		if _, err := tx.Exec(`INSERT INTO device_grants (device_id, capability, granted_by, granted_at)
			VALUES (?, ?, ?, ?)`, deviceID, capability, reviewerID, now); err != nil {
			return nil, err
		}
	}

	// Keep the target service and public ingress lifecycle aligned with the
	// approved capabilities. Existing ingress allocations are deliberately
	// disabled when public RDP is removed; re-enabling them remains an explicit
	// administrator action.
	if slices.Contains(approved, "rdp.host") {
		var serviceID string
		serviceErr := tx.QueryRow(`SELECT id FROM rdp_services WHERE device_id = ?`, deviceID).Scan(&serviceID)
		switch {
		case errors.Is(serviceErr, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO rdp_services
				(id, device_id, name, target_host, target_port, enabled, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, TRUE, ?, ?)`,
				"rdpsvc_"+uuid.NewString(), deviceID, device.Name, "127.0.0.1", 3389, now, now); err != nil {
				return nil, err
			}
		case serviceErr != nil:
			return nil, serviceErr
		default:
			if _, err := tx.Exec(`UPDATE rdp_services SET enabled = TRUE, updated_at = ? WHERE id = ?`, now, serviceID); err != nil {
				return nil, err
			}
		}
	} else {
		if _, err := tx.Exec(`UPDATE rdp_services SET enabled = FALSE, updated_at = ? WHERE device_id = ?`, now, deviceID); err != nil {
			return nil, err
		}
	}
	if !slices.Contains(approved, "rdp.host") || !slices.Contains(approved, "rdp.public") {
		if _, err := tx.Exec(`UPDATE rdp_port_allocations SET status = 'disabled', updated_at = ?
			WHERE service_id IN (SELECT id FROM rdp_services WHERE device_id = ?)`, now, deviceID); err != nil {
			return nil, err
		}
	}

	ownerID := owner.String
	if _, err := tx.Exec(`DELETE FROM rdp_access_grants
		WHERE controller_device_id = ? OR target_device_id = ?`, deviceID, deviceID); err != nil {
		return nil, err
	}
	if ownerID != "" {
		if err := syncOwnerRDPGrants(tx, ownerID, deviceID, approved, reviewerID, now); err != nil {
			return nil, err
		}
	}
	if err := insertAuthorizationAudit(tx, "device.capabilities.update", reviewerID, "device", deviceID,
		map[string]any{"before": previous, "after": approved}, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	device.OwnerUserID = ownerID
	device.RequestedCapabilities = requested
	device.ApprovedCapabilities = approved
	device.DeviceMode = modeForCapabilities(approved)
	device.Enabled = true
	device.UpdatedAt = now
	if lastSeen.Valid {
		value := lastSeen.Time
		device.LastSeenAt = &value
	}
	return &device, nil
}

type sqlExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertAuthorizationAudit(executor sqlExecutor, action, actor, targetType, targetID string, details any, at time.Time) error {
	encoded, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = executor.Exec(`INSERT INTO authorization_audit
		(id, action, actor_user_id, target_type, target_id, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "auth_"+uuid.NewString(), action, actor, targetType, targetID, string(encoded), at)
	return err
}

func filterApprovedCapabilities(selected, requested []string) []string {
	if len(selected) == 0 {
		// Least privilege: a one-click approval grants ordinary client access.
		// Extra capabilities (exit, RDP host/public ingress) must be explicit.
		selected = []string{"proxy.client"}
		if !slices.Contains(requested, "proxy.client") && len(requested) > 0 {
			selected = requested[:1]
		}
	}
	allowed := map[string]bool{
		"proxy.client": true, "proxy.exit": true, "rdp.controller": true,
		"rdp.host": true, "rdp.public": true,
	}
	out := make([]string, 0, len(selected))
	for _, capability := range normalizeCapabilities(selected) {
		if allowed[capability] && slices.Contains(requested, capability) {
			out = append(out, capability)
		}
	}
	return out
}

func validateCapabilityDependencies(capabilities []string) error {
	if slices.Contains(capabilities, "rdp.public") && !slices.Contains(capabilities, "rdp.host") {
		return errors.New("rdp.public requires rdp.host")
	}
	return nil
}

// syncOwnerRDPGrants keeps the first M2 access model deliberately simple: an
// administrator-approved device can reach other RDP-capable devices assigned
// to the same owner. The explicit rows make the authorization matrix visible
// and leave room for per-target grants in the next milestone.
func syncOwnerRDPGrants(tx *sql.Tx, ownerID, newDeviceID string, newCapabilities []string, reviewerID string, now time.Time) error {
	newController := slices.Contains(newCapabilities, "rdp.controller")
	newTarget := slices.Contains(newCapabilities, "rdp.host")
	rows, err := tx.Query(`SELECT id, approved_capabilities FROM devices WHERE owner_user_id = ? AND approval_state = 'approved' AND id <> ?`, ownerID, newDeviceID)
	if err != nil {
		return err
	}
	type existing struct {
		id   string
		caps []string
	}
	var devices []existing
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		caps, err := decodeCapabilities(raw)
		if err != nil {
			_ = rows.Close()
			return err
		}
		devices = append(devices, existing{id: id, caps: caps})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range devices {
		if newController && slices.Contains(item.caps, "rdp.host") {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO rdp_access_grants (controller_device_id, target_device_id, granted_by, created_at) VALUES (?, ?, ?, ?)`, newDeviceID, item.id, reviewerID, now); err != nil {
				return err
			}
		}
		if newTarget && slices.Contains(item.caps, "rdp.controller") {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO rdp_access_grants (controller_device_id, target_device_id, granted_by, created_at) VALUES (?, ?, ?, ?)`, item.id, newDeviceID, reviewerID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func fallbackDeviceName(name string) string {
	if name == "" {
		return "RelayProxy Agent"
	}
	return name
}
