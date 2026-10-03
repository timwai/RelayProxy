package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"relayproxy/server/repository"
)

type deviceIdentityGrantRequest struct {
	TargetDeviceID    string   `json:"targetDeviceId"`
	GranteeIdentityID string   `json:"granteeIdentityId"`
	Features          []string `json:"features"`
	ExpiresAt         string   `json:"expiresAt,omitempty"`
}

type deviceIdentityGrantUpdateRequest struct {
	GranteeIdentityID *string   `json:"granteeIdentityId,omitempty"`
	Features          *[]string `json:"features,omitempty"`
	ExpiresAt         *string   `json:"expiresAt,omitempty"`
	ClearExpiresAt    bool      `json:"clearExpiresAt,omitempty"`
	Revision          int64     `json:"revision"`
}

func parseGrantExpiry(raw string) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, errors.New("expiresAt must be an RFC3339 timestamp")
	}
	value = value.UTC()
	return &value, nil
}

func (r *Router) handleListDeviceIdentityGrants(w http.ResponseWriter, req *http.Request) {
	items, err := r.db.ListDeviceIdentityGrants(
		req.URL.Query().Get("targetDeviceId"),
		req.URL.Query().Get("granteeIdentityId"),
		req.URL.Query().Get("feature"),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (r *Router) handleCreateDeviceIdentityGrant(w http.ResponseWriter, req *http.Request) {
	var body deviceIdentityGrantRequest
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	expiresAt, err := parseGrantExpiry(body.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.CreateDeviceIdentityGrant(
		body.TargetDeviceID, body.GranteeIdentityID, actor, body.Features, expiresAt,
	)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "target device or grantee identity not found")
		case errors.Is(err, repository.ErrDeviceIdentityGrantExists):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	r.notifyDeviceIdentityGrantChanged(item.TargetDeviceID, item.GranteeIdentityID)
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleUpdateDeviceIdentityGrant(w http.ResponseWriter, req *http.Request) {
	var body deviceIdentityGrantUpdateRequest
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.GranteeIdentityID == nil && body.Features == nil && body.ExpiresAt == nil && !body.ClearExpiresAt {
		writeError(w, http.StatusBadRequest, "at least one grant field must be changed")
		return
	}
	current, err := r.db.GetDeviceIdentityGrant(req.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device identity grant not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load device identity grant")
		}
		return
	}

	var expiresAt *time.Time
	if body.ExpiresAt != nil {
		expiresAt, err = parseGrantExpiry(*body.ExpiresAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if expiresAt == nil {
			body.ClearExpiresAt = true
		}
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.UpdateDeviceIdentityGrant(req.PathValue("id"), actor, repository.DeviceIdentityGrantUpdate{
		GranteeIdentityID: body.GranteeIdentityID,
		Features:          body.Features,
		ExpiresAt:         expiresAt,
		ClearExpiresAt:    body.ClearExpiresAt,
		Revision:          body.Revision,
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "device identity grant not found")
		case errors.Is(err, repository.ErrDeviceIdentityGrantExists),
			errors.Is(err, repository.ErrDeviceIdentityGrantRevision):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	r.notifyDeviceIdentityGrantChanged(current.TargetDeviceID, current.GranteeIdentityID)
	if item.GranteeIdentityID != current.GranteeIdentityID {
		r.notifyDeviceIdentityGrantChanged(item.TargetDeviceID, item.GranteeIdentityID)
	}
	writeJSON(w, http.StatusOK, item)
}

func (r *Router) handleDeleteDeviceIdentityGrant(w http.ResponseWriter, req *http.Request) {
	current, err := r.db.GetDeviceIdentityGrant(req.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device identity grant not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load device identity grant")
		}
		return
	}
	revision := int64(0)
	if raw := strings.TrimSpace(req.URL.Query().Get("revision")); raw != "" {
		revision, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || revision < 1 {
			writeError(w, http.StatusBadRequest, "revision must be a positive integer")
			return
		}
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	deleted, err := r.db.DeleteDeviceIdentityGrant(current.ID, actor, revision)
	if err != nil {
		if errors.Is(err, repository.ErrDeviceIdentityGrantRevision) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if deleted {
		r.notifyDeviceIdentityGrantChanged(current.TargetDeviceID, current.GranteeIdentityID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": current.ID, "deleted": deleted})
}

func (r *Router) notifyDeviceIdentityGrantChanged(targetDeviceID, granteeIdentityID string) {
	if r.onDeviceIdentityGrantChanged != nil {
		r.onDeviceIdentityGrantChanged(targetDeviceID, granteeIdentityID)
	}
}
