package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"relayproxy/server/repository"
)

type systemIdentityGrantRequest struct {
	ResourceID        string   `json:"resourceId"`
	GranteeIdentityID string   `json:"granteeIdentityId"`
	Features          []string `json:"features"`
	ExpiresAt         string   `json:"expiresAt,omitempty"`
}

type systemIdentityGrantUpdateRequest struct {
	GranteeIdentityID *string   `json:"granteeIdentityId,omitempty"`
	Features          *[]string `json:"features,omitempty"`
	ExpiresAt         *string   `json:"expiresAt,omitempty"`
	ClearExpiresAt    bool      `json:"clearExpiresAt,omitempty"`
	Revision          int64     `json:"revision"`
}

func (r *Router) handleListSystemIdentityGrants(w http.ResponseWriter, req *http.Request) {
	items, err := r.db.ListSystemIdentityGrants(
		req.URL.Query().Get("resourceId"),
		req.URL.Query().Get("granteeIdentityId"),
	)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (r *Router) handleCreateSystemIdentityGrant(w http.ResponseWriter, req *http.Request) {
	var body systemIdentityGrantRequest
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
	item, err := r.db.CreateSystemIdentityGrant(
		body.ResourceID, body.GranteeIdentityID, actor, body.Features, expiresAt,
	)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "grantee identity not found")
		case errors.Is(err, repository.ErrSystemIdentityGrantExists):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(item.GranteeIdentityID, "")
	}
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleUpdateSystemIdentityGrant(w http.ResponseWriter, req *http.Request) {
	var body systemIdentityGrantUpdateRequest
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.GranteeIdentityID == nil && body.Features == nil && body.ExpiresAt == nil && !body.ClearExpiresAt {
		writeError(w, http.StatusBadRequest, "at least one grant field must be changed")
		return
	}
	current, err := r.db.GetSystemIdentityGrant(req.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "system identity grant not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load system identity grant")
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
	item, err := r.db.UpdateSystemIdentityGrant(req.PathValue("id"), actor, repository.SystemIdentityGrantUpdate{
		GranteeIdentityID: body.GranteeIdentityID,
		Features:          body.Features,
		ExpiresAt:         expiresAt,
		ClearExpiresAt:    body.ClearExpiresAt,
		Revision:          body.Revision,
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "system identity grant not found")
		case errors.Is(err, repository.ErrSystemIdentityGrantExists),
			errors.Is(err, repository.ErrSystemIdentityGrantRevision):
			writeError(w, http.StatusConflict, err.Error())
		default:
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(current.GranteeIdentityID, "")
		if item.GranteeIdentityID != current.GranteeIdentityID {
			r.onIdentityAuthorizationChanged(item.GranteeIdentityID, "")
		}
	}
	writeJSON(w, http.StatusOK, item)
}

func (r *Router) handleDeleteSystemIdentityGrant(w http.ResponseWriter, req *http.Request) {
	current, err := r.db.GetSystemIdentityGrant(req.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "system identity grant not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load system identity grant")
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
	deleted, err := r.db.DeleteSystemIdentityGrant(current.ID, actor, revision)
	if err != nil {
		if errors.Is(err, repository.ErrSystemIdentityGrantRevision) {
			writeError(w, http.StatusConflict, err.Error())
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if deleted && r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(current.GranteeIdentityID, "")
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": current.ID, "deleted": deleted})
}

// Keep encoding/json referenced here so decode behavior stays consistent with
// the sibling grant handler even if request parsing is later inlined.
var _ = json.Valid
