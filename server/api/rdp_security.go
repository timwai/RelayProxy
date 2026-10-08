package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"relayproxy/server/repository"
)

// RDP security APIs are administrator-only. An identity login must not gain
// visibility into other tenants' source addresses or mutate global bans.
func rdpSecurityPagination(req *http.Request) (int, int, error) {
	page, size := 1, 20
	if raw := req.URL.Query().Get("page"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("invalid page")
		}
		page = n
	}
	if raw := req.URL.Query().Get("pageSize"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("invalid pageSize")
		}
		size = n
	}
	if page < 1 || page > 100000 || size < 1 || size > 100 {
		return 0, 0, errors.New("page must be 1-100000; pageSize must be 1-100")
	}
	return page, size, nil
}

func (r *Router) handleListRDPSecurityGroups(w http.ResponseWriter, req *http.Request) {
	page, size, err := rdpSecurityPagination(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	groups, err := r.db.ListRDPSecuritySourceGroups(req.URL.Query().Get("search"), page, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP source IP groups")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (r *Router) handleListRDPSecurityLogs(w http.ResponseWriter, req *http.Request) {
	page, size, err := rdpSecurityPagination(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ip := strings.TrimSpace(req.URL.Query().Get("ip"))
	if ip != "" {
		address, parseErr := netip.ParseAddr(ip)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid source IP")
			return
		}
		ip = address.Unmap().String()
	}
	logs, err := r.db.ListRDPSecurityLogPage(ip, page, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP connections")
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// Deleting connection audit is separate from deleting bans, allowlist entries,
// configured ingress or the unrelated regular proxy connection audit.
func (r *Router) handleClearRDPSecurityLogs(w http.ResponseWriter, req *http.Request) {
	ip := strings.TrimSpace(req.URL.Query().Get("ip"))
	if ip != "" {
		address, err := netip.ParseAddr(ip)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid source IP")
			return
		}
		ip = address.Unmap().String()
	}
	var count int64
	var err error
	if r.onRDPSecurityLogsClear != nil {
		count, err = r.onRDPSecurityLogsClear(req.Context(), ip)
	} else {
		count, err = r.db.DeleteRDPSecurityLogs(ip)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear RDP connection audit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": count, "sourceIp": ip})
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
