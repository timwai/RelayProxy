package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	browsersync "relayproxy/server/browser_sync"
)

// browserAdminContext is only used behind the requireAuth + requireAdmin
// middleware. Client payloads cannot specify the audit operator.
func browserAdminContext(req *http.Request) context.Context {
	actorID, _ := req.Context().Value(userContextKey).(string)
	return browsersync.WithAdminActor(req.Context(), actorID)
}

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
	if err := r.browserSync.Store.ApproveEnrollment(browserAdminContext(req), req.PathValue("id"), body.IdentityID, body.Send, body.Receive); err != nil {
		if errors.Is(err, browsersync.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pending browser not found")
			return
		}
		if errors.Is(err, browsersync.ErrBrowserIdentityQuota) {
			writeError(w, http.StatusConflict, "browser identity device capacity reached")
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
	if err := r.browserSync.Store.Revoke(browserAdminContext(req), req.PathValue("id")); err != nil {
		if errors.Is(err, browsersync.ErrNotFound) {
			writeError(w, http.StatusNotFound, "browser device not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "unable to revoke browser device")
		return
	}
	r.browserSync.DisconnectDevice(req.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "state": "revoked"})
}

func (r *Router) handleBrowserSyncDelete(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if err := r.browserSync.Store.DeleteRevokedDevice(browserAdminContext(req), id); err != nil {
		switch {
		case errors.Is(err, browsersync.ErrNotFound):
			writeError(w, http.StatusNotFound, "browser device not found")
		case errors.Is(err, browsersync.ErrDeleteRequiresRevoke):
			writeError(w, http.StatusConflict, "revoke browser authorization before deleting")
		default:
			writeError(w, http.StatusInternalServerError, "unable to delete browser device")
		}
		return
	}
	r.browserSync.DisconnectDevice(id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": "deleted"})
}
func (r *Router) handleBrowserSyncExtensionDelete(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if err := r.browserSync.Store.DeleteRevokedExtension(req.Context(), id); err != nil {
		switch {
		case errors.Is(err, browsersync.ErrNotFound):
			writeError(w, http.StatusNotFound, "extension not found")
		case errors.Is(err, browsersync.ErrDeleteRequiresRevoke):
			writeError(w, http.StatusConflict, "revoke extension before deleting")
		case errors.Is(err, browsersync.ErrExtensionStillLinked):
			writeError(w, http.StatusConflict, "delete linked browser devices before deleting extension")
		default:
			writeError(w, http.StatusInternalServerError, "unable to delete extension")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "state": "deleted"})
}
func (r *Router) handleBrowserSyncExtensions(w http.ResponseWriter, req *http.Request) {
	items, err := r.browserSync.Store.Extensions(req.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot list extensions")
		return
	}
	writeJSON(w, http.StatusOK, items)
}
func (r *Router) handleBrowserSyncExtensionRevoke(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("id")
	if err := r.browserSync.Store.SetExtensionApproved(req.Context(), id, false); err != nil {
		writeError(w, http.StatusBadRequest, "approved extension not found")
		return
	}
	// Legacy origin-wide disable must also tear down existing WSS sessions,
	// not merely block the next inbound message from an already-online device.
	devices, err := r.browserSync.Store.Devices(req.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "extension revoked; unable to enumerate browser sessions")
		return
	}
	for _, device := range devices {
		if device.ExtensionID == id {
			r.browserSync.DisconnectDevice(device.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "revoked"})
}

// Admin-only management metadata. Never return encrypted offers, browser keys,
// tokens, credential material or arbitrary client-supplied error strings.
func (r *Router) handleBrowserSyncRules(w http.ResponseWriter, req *http.Request) {
	// Preserve the original array response for legacy management clients.
	// The Server Web opts into pagination with a limit query parameter.
	values := req.URL.Query()
	if !values.Has("limit") && !values.Has("offset") &&
		!values.Has("identityId") && !values.Has("status") && !values.Has("q") {
		items, err := r.browserSync.Store.AdminRules(req.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "cannot list browser sync rules")
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	limit := 50
	if raw := values.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "invalid browser rule page size")
			return
		}
		limit = n
	}
	offset := 0
	if raw := values.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 100000 {
			writeError(w, http.StatusBadRequest, "invalid browser rule offset")
			return
		}
		offset = n
	}
	items, err := r.browserSync.Store.AdminRulesPage(req.Context(), limit, offset,
		values.Get("identityId"), values.Get("status"), values.Get("q"))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid browser rule") {
			writeError(w, http.StatusBadRequest, "invalid browser rule filter")
		} else {
			writeError(w, http.StatusInternalServerError, "cannot list browser sync rules")
		}
		return
	}
	next := -1
	if len(items) == limit {
		next = offset + len(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextOffset": next})
}

func (r *Router) handleBrowserSyncRuleAction(w http.ResponseWriter, req *http.Request) {
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	action := parts[len(parts)-1]
	if req.Body != nil && req.ContentLength > 0 {
		var body struct{}
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid browser rule action")
			return
		}
	}
	if err := r.browserSync.Store.AdminRuleAction(browserAdminContext(req), req.PathValue("id"), action); err != nil {
		if errors.Is(err, browsersync.ErrNotFound) {
			writeError(w, http.StatusNotFound, "browser rule not found")
		} else if errors.Is(err, browsersync.ErrRuleDenied) {
			writeError(w, http.StatusConflict, "browser rule cannot transition in its current state")
		} else {
			writeError(w, http.StatusInternalServerError, "unable to change browser rule")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": req.PathValue("id"), "action": action})
}

func (r *Router) handleBrowserSyncStatus(w http.ResponseWriter, req *http.Request) {
	stats, err := r.browserSync.Store.AdminDeviceStats(req.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unable to read browser activity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"online": r.browserSync.OnlineSnapshot(),
		"stats":  stats,
	})
}

func (r *Router) handleBrowserSyncAudit(w http.ResponseWriter, req *http.Request) {
	limit := 50
	if value := req.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid browser audit limit")
			return
		}
		limit = parsed
	}
	var before int64
	if value := req.URL.Query().Get("before"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid browser audit cursor")
			return
		}
		before = parsed
	}
	items, err := r.browserSync.Store.AdminAuditFiltered(req.Context(), before, limit,
		req.URL.Query().Get("type"), req.URL.Query().Get("device"))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid browser audit") {
			writeError(w, http.StatusBadRequest, "invalid browser audit filter")
		} else {
			writeError(w, http.StatusInternalServerError, "unable to load browser audit")
		}
		return
	}
	next := int64(0)
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

// browserAuditCSVCell prevents spreadsheet formulas in values not guaranteed
// to be server-generated UUIDs, particularly administrator account IDs.
func browserAuditCSVCell(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + value
	}
	return value
}

func (r *Router) handleBrowserSyncAuditExport(w http.ResponseWriter, req *http.Request) {
	entries, err := r.browserSync.Store.AdminAuditExport(req.Context(),
		req.URL.Query().Get("type"), req.URL.Query().Get("device"))
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid browser audit") {
			writeError(w, http.StatusBadRequest, "invalid audit export filter")
		} else {
			writeError(w, http.StatusInternalServerError, "cannot export browser audit")
		}
		return
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	if err := writer.Write([]string{"id", "time_utc", "event", "actor_id",
		"device_id", "peer_id", "rule_id", "outcome", "error_code"}); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot serialize browser audit")
		return
	}
	for _, entry := range entries {
		values := []string{
			strconv.FormatInt(entry.ID, 10), entry.CreatedAt.UTC().Format(time.RFC3339),
			entry.Type, entry.ActorID, entry.DeviceID, entry.PeerID, entry.RuleID, entry.Outcome, entry.ErrorCode,
		}
		for i := range values {
			values[i] = browserAuditCSVCell(values[i])
		}
		if err := writer.Write(values); err != nil {
			writeError(w, http.StatusInternalServerError, "cannot serialize browser audit")
			return
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		writeError(w, http.StatusInternalServerError, "cannot serialize browser audit")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="relayproxy-browser-sync-audit.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (r *Router) handleBrowserSyncAuditClear(w http.ResponseWriter, req *http.Request) {
	count, err := r.browserSync.Store.ClearAdminAudit(browserAdminContext(req))
	if err != nil {
		if errors.Is(err, browsersync.ErrRuleDenied) {
			writeError(w, http.StatusForbidden, "administrator required to clear browser audit")
		} else {
			writeError(w, http.StatusInternalServerError, "unable to clear browser audit")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": count})
}
