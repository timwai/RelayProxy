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

// RDP security APIs are administrator-only. An identity login must not gain
// visibility into other tenants' source addresses or mutate global bans.
func (r *Router) handleListRDPSecurityLogs(w http.ResponseWriter, req *http.Request) {
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	rows, err := r.db.ListRDPSecurityLogs(strings.TrimSpace(req.URL.Query().Get("ip")), strings.TrimSpace(req.URL.Query().Get("ingressId")), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP connections")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleListRDPAuthFailures exposes audited Windows events, including those
// with unknown public source IP. Only administrators may view usernames.
func (r *Router) handleListRDPAuthFailures(w http.ResponseWriter, req *http.Request) {
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	rows, err := r.db.ListRDPAuthFailures(strings.TrimSpace(req.URL.Query().Get("ip")),
		strings.TrimSpace(req.URL.Query().Get("ingressId")), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list Windows login failures")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (r *Router) handleListRDPSecurityBans(w http.ResponseWriter, req *http.Request) {
	rows, err := r.db.ListActiveRDPSecurityBans()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP bans")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (r *Router) handleCreateRDPSecurityBan(w http.ResponseWriter, req *http.Request) {
	var body struct {
		CIDR            string `json:"cidr"`
		IngressID       string `json:"ingressId"`
		Kind            string `json:"kind"`
		Reason          string `json:"reason"`
		DurationSeconds int    `json:"durationSeconds"`
	}
	if err := decodeJSON(w, req, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Kind == "" {
		body.Kind = "manual"
	}
	if body.Kind != "manual" && body.Kind != "allow" {
		writeError(w, http.StatusBadRequest, "only manual bans and allowlist entries may be created via API")
		return
	}
	if body.DurationSeconds < 0 || body.DurationSeconds > 31536000 {
		writeError(w, http.StatusBadRequest, "invalid durationSeconds")
		return
	}
	var expiry *time.Time
	if body.DurationSeconds > 0 {
		t := time.Now().Add(time.Duration(body.DurationSeconds) * time.Second).UTC()
		expiry = &t
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.CreateRDPSecurityBan(body.CIDR, strings.TrimSpace(body.IngressID), body.Kind, strings.TrimSpace(body.Reason), actor, expiry)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.onRDPSecurityReload != nil {
		if err := r.onRDPSecurityReload(); err != nil {
			writeError(w, http.StatusInternalServerError, "entry persisted but failed to refresh runtime security: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, item)
}

func (r *Router) handleRevokeRDPSecurityBan(w http.ResponseWriter, req *http.Request) {
	actor, _ := req.Context().Value(userContextKey).(string)
	if err := r.db.RevokeRDPSecurityBan(req.PathValue("id"), actor); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ban not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.onRDPSecurityReload != nil {
		if err := r.onRDPSecurityReload(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (r *Router) handleListRDPSecurityRules(w http.ResponseWriter, req *http.Request) {
	rules, err := r.db.ListRDPSecurityRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP rules")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (r *Router) handleUpdateRDPSecurityRule(w http.ResponseWriter, req *http.Request) {
	var rule repository.RDPSecurityRule
	if err := decodeJSON(w, req, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rule.ID = req.PathValue("id")
	if err := r.db.UpdateRDPSecurityRule(rule); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.onRDPSecurityReload != nil {
		if err := r.onRDPSecurityReload(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, rule)
}
