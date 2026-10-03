package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"relayproxy/server/repository"
)

func (r *Router) handleListIdentities(w http.ResponseWriter, req *http.Request) {
	items, err := r.db.ListIdentities()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list identities")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (r *Router) handleCreateIdentity(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.CreateIdentity(body.Name, actor, body.Capabilities)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleUpdateIdentity(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Name           *string   `json:"name"`
		Status         *string   `json:"status"`
		Capabilities   *[]string `json:"capabilities"`
		PolicyRevision int64     `json:"policyRevision"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == nil && body.Status == nil && body.Capabilities == nil {
		writeError(w, http.StatusBadRequest, "at least one identity field must be changed")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.UpdateIdentity(req.PathValue("id"), actor, repository.IdentityUpdate{
		Name: body.Name, Status: body.Status, Capabilities: body.Capabilities, PolicyRevision: body.PolicyRevision,
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "identity not found")
		case errors.Is(err, repository.ErrIdentityRevisionConflict):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(item.ID, "")
	}
	writeJSON(w, http.StatusOK, item)
}

func (r *Router) handleListIdentityAccessKeys(w http.ResponseWriter, req *http.Request) {
	if _, err := r.db.GetIdentity(req.PathValue("id")); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "identity not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load identity")
		}
		return
	}
	items, err := r.db.ListIdentityAccessKeys(req.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list access keys")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (r *Router) handleIssueIdentityAccessKey(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Label     string `json:"label"`
		ExpiresAt string `json:"expiresAt"`
	}
	if req.ContentLength != 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	var expiresAt *time.Time
	if strings.TrimSpace(body.ExpiresAt) != "" {
		value, err := time.Parse(time.RFC3339, strings.TrimSpace(body.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "expiresAt must be an RFC3339 timestamp")
			return
		}
		value = value.UTC()
		expiresAt = &value
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.IssueIdentityAccessKey(req.PathValue("id"), actor, body.Label, expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "identity not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	// AccessKey is intentionally returned only by this endpoint. List responses
	// never expose the digest or the original secret.
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleRevokeIdentityAccessKey(w http.ResponseWriter, req *http.Request) {
	actor, _ := req.Context().Value(userContextKey).(string)
	err := r.db.RevokeIdentityAccessKey(req.PathValue("id"), req.PathValue("keyId"), actor)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "identity access key not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(req.PathValue("id"), req.PathValue("keyId"))
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": req.PathValue("keyId"), "status": "revoked",
	})
}

func (r *Router) handleSetDeviceIdentity(w http.ResponseWriter, req *http.Request) {
	var body struct {
		IdentityID string `json:"identityId"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	deviceID := req.PathValue("id")
	var item *repository.DeviceIdentitySummary
	err := r.sessions.ChangeDeviceAuthorization(deviceID, true, func() error {
		var changeErr error
		item, changeErr = r.db.SetDeviceIdentity(deviceID, body.IdentityID, actor)
		return changeErr
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device or identity not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if r.onDeviceAuthorizationChanged != nil {
		r.onDeviceAuthorizationChanged(deviceID)
	}
	writeJSON(w, http.StatusOK, item)
}
