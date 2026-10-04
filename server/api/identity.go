package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"relayproxy/server/repository"
	"relayproxy/server/service"
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
		ShortID  string `json:"shortId"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	if !validIdentityPassword(body.Password) {
		writeError(w, http.StatusBadRequest, "password must contain 8 to 128 characters and cannot be blank")
		return
	}
	hash, err := service.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect identity password")
		return
	}
	item, err := r.db.CreateIdentityWithLogin(body.ShortID, body.Name, actor, hash)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func validIdentityPassword(password string) bool {
	length := utf8.RuneCountInString(password)
	return utf8.ValidString(password) && length >= 8 && length <= 128 && strings.TrimSpace(password) != ""
}

func (r *Router) handleResetIdentityPassword(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validIdentityPassword(body.Password) {
		writeError(w, http.StatusBadRequest, "password must contain 8 to 128 characters and cannot be blank")
		return
	}
	hash, err := service.HashPassword(body.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to protect identity password")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	if err := r.db.ConfigureIdentityLogin(req.PathValue("id"), hash, actor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "identity not found")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	if userID, err := r.db.GetIdentityLoginUserID(req.PathValue("id")); err == nil {
		r.authService.RevokeUserSessions(userID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.PathValue("id"), "loginConfigured": true})
}

func (r *Router) handleListIdentityOptions(w http.ResponseWriter, _ *http.Request) {
	items, err := r.db.ListIdentities()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list identities")
		return
	}
	result := make([]map[string]string, 0, len(items))
	for _, item := range items {
		if item.Status == repository.IdentityStatusActive {
			result = append(result, map[string]string{"id": item.ID, "shortId": item.ShortID, "name": item.Name})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) handleUpdateIdentity(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Name           *string `json:"name"`
		Status         *string `json:"status"`
		PolicyRevision int64   `json:"policyRevision"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == nil && body.Status == nil {
		writeError(w, http.StatusBadRequest, "at least one identity field must be changed")
		return
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.UpdateIdentity(req.PathValue("id"), actor, repository.IdentityUpdate{
		Name: body.Name, Status: body.Status, PolicyRevision: body.PolicyRevision,
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
	if body.Status != nil && r.onIdentityAuthorizationChanged != nil {
		r.onIdentityAuthorizationChanged(item.ID)
	}
	if body.Status != nil {
		if userID, err := r.db.GetIdentityLoginUserID(item.ID); err == nil {
			r.authService.RevokeUserSessions(userID)
		}
	}
	writeJSON(w, http.StatusOK, item)
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

	previous, err := r.db.GetDeviceIdentitySummary(deviceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "device not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load device identity")
		}
		return
	}
	previousGrants, err := r.db.ListDeviceIdentityGrants(deviceID, "", "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load device identity grants")
		return
	}

	var item *repository.DeviceIdentitySummary
	err = r.sessions.ChangeDeviceAuthorization(deviceID, true, func() error {
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

	if item != nil && previous.IdentityID != item.IdentityID && r.onIdentityAuthorizationChanged != nil {
		// Moving a target changes same-identity access on both sides and also
		// deletes its explicit cross-identity shares. Invalidate every affected
		// identity so resource inventories and live paths are refreshed now.
		affected := map[string]bool{}
		if previous.IdentityID != "" {
			affected[previous.IdentityID] = true
		}
		if item.IdentityID != "" {
			affected[item.IdentityID] = true
		}
		for _, grant := range previousGrants {
			if grant != nil && grant.GranteeIdentityID != "" {
				affected[grant.GranteeIdentityID] = true
			}
		}
		for identityID := range affected {
			r.onIdentityAuthorizationChanged(identityID)
		}
	}
	writeJSON(w, http.StatusOK, item)
}
