package api

import (
	"encoding/json"
	"errors"
	"net/http"

	browsersync "relayproxy/server/browser_sync"
)

// Admin endpoints are protected by the existing requireAuth+requireAdmin
// middleware. Browser devices never inherit Agent proxy or RDP capabilities.
func (r *Router) handleBrowserSyncDevices(w http.ResponseWriter, req *http.Request) {
	items, err := r.browserSync.Store.Devices(req.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to list browser enrollments")
		return
	}
	if items == nil {
		items = []browsersync.BrowserDevice{}
	}
	// Administrative device inventory includes public keys, never credentials.
	writeJSON(w, http.StatusOK, items)
}
func (r *Router) handleBrowserSyncApprove(w http.ResponseWriter, req *http.Request) {
	var body struct {
		IdentityID string `json:"identityId"`
		Send       bool   `json:"send"`
		Receive    bool   `json:"receive"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid approval request")
		return
	}
	if err := r.browserSync.Store.Approve(req.Context(), req.PathValue("id"), body.IdentityID, body.Send, body.Receive); err != nil {
		if errors.Is(err, browsersync.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pending browser not found")
			return
		}
		writeError(w, http.StatusBadRequest, "browser device approval failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "state": "approved"})
}
func (r *Router) handleBrowserSyncRevoke(w http.ResponseWriter, req *http.Request) {
	if req.Body != nil && req.ContentLength > 0 {
		var body any
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1024)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid revocation request")
			return
		}
	}
	if err := r.browserSync.Store.Revoke(req.Context(), req.PathValue("id")); err != nil {
		if errors.Is(err, browsersync.ErrNotFound) {
			writeError(w, http.StatusNotFound, "browser device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "unable to revoke browser device")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "state": "revoked"})
}
