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
	query := req.URL.Query()
	sortKey, order := query.Get("sort"), query.Get("order")
	if !repository.ValidRDPSourceGroupSort(sortKey) || (order != "" && order != "asc" && order != "desc") {
		writeError(w, http.StatusBadRequest, "invalid sort or order")
		return
	}
	groups, err := r.db.ListRDPSecuritySourceGroupsSorted(query.Get("search"), sortKey, order, page, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list RDP source IP groups")
		return
	}
	// Mark sources already covered by an active block so the console can
	// hide redundant ban actions. Allowlist entries are not blocks.
	if bans, err := r.db.ListActiveRDPSecurityBans(); err == nil && len(groups.Items) > 0 {
		compiled := make([]struct {
			ban    repository.RDPSecurityBan
			prefix netip.Prefix
		}, 0, len(bans))
		for _, ban := range bans {
			if ban.Kind == "allow" {
				continue
			}
			if prefix, err := netip.ParsePrefix(ban.CIDR); err == nil {
				compiled = append(compiled, struct {
					ban    repository.RDPSecurityBan
					prefix netip.Prefix
				}{ban, prefix})
			}
		}
		for i := range groups.Items {
			address, err := netip.ParseAddr(groups.Items[i].SourceIP)
			if err != nil {
				continue
			}
			address = address.Unmap()
			for _, entry := range compiled {
				if !entry.prefix.Contains(address) {
					continue
				}
				// Prefer a permanent and global decision when several apply.
				current := groups.Items[i]
				better := current.BanKind == "" ||
					(entry.ban.ExpiresAt == nil && current.BanExpiresAt != nil) ||
					(entry.ban.Kind == "manual" && current.BanKind != "manual" && (entry.ban.ExpiresAt == nil) == (current.BanExpiresAt == nil))
				if better {
					groups.Items[i].BanKind = entry.ban.Kind
					groups.Items[i].BanExpiresAt = entry.ban.ExpiresAt
				}
			}
		}
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

// handleListRDPSecurityBans keeps the legacy full-array response unless the
// caller asks for a page; the console always paginates (GeoIP can produce
// thousands of permanent entries).
func (r *Router) handleListRDPSecurityBans(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	if query.Has("page") || query.Has("pageSize") {
		page, size, err := rdpSecurityPagination(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := r.db.ListRDPSecurityBanPage(repository.RDPSecurityBanFilter{
			Search: query.Get("search"), Kind: query.Get("kind"), Scope: query.Get("scope"), Expiry: query.Get("expiry"),
			Page: page, Size: size,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
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
		ClearLogs       bool   `json:"clearLogs"`
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
	item, err := r.db.CreateRDPSecurityBanWithOptions(repository.RDPSecurityBanInput{CIDR: body.CIDR, IngressID: strings.TrimSpace(body.IngressID),
		Kind: body.Kind, Reason: strings.TrimSpace(body.Reason), Actor: actor, ExpiresAt: expiry, ClearLogs: body.ClearLogs})
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
	// The runtime suppresses new rows as soon as it reloads; purge history
	// after that so queued rows cannot be written back.
	if item.ClearLogs {
		var clearErr error
		if r.onRDPSecurityLogsClear != nil {
			_, clearErr = r.onRDPSecurityLogsClear(req.Context(), item.CIDR)
		} else {
			_, clearErr = r.db.DeleteRDPSecurityLogsByCIDR(item.CIDR)
		}
		if clearErr != nil {
			writeError(w, http.StatusInternalServerError, "entry persisted but failed to clear connection audit: "+clearErr.Error())
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

func (r *Router) reloadRDPSecurity(w http.ResponseWriter) bool {
	if r.onRDPSecurityReload != nil {
		if err := r.onRDPSecurityReload(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return false
		}
	}
	return true
}

// handleMakeRDPSecurityBanPermanent is the one-click "make permanent" action
// for an existing temporary entry.
func (r *Router) handleMakeRDPSecurityBanPermanent(w http.ResponseWriter, req *http.Request) {
	clearLogs := req.URL.Query().Get("clearLogs") == "true" || req.URL.Query().Get("clearLogs") == "1"
	if req.ContentLength > 0 {
		var body struct {
			ClearLogs bool `json:"clearLogs"`
		}
		if err := decodeJSON(w, req, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		clearLogs = clearLogs || body.ClearLogs
	}
	actor, _ := req.Context().Value(userContextKey).(string)
	item, err := r.db.MakeRDPSecurityBanPermanent(req.PathValue("id"), actor, clearLogs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ban not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !r.reloadRDPSecurity(w) {
		return
	}
	if item.ClearLogs {
		var clearErr error
		if r.onRDPSecurityLogsClear != nil {
			_, clearErr = r.onRDPSecurityLogsClear(req.Context(), item.CIDR)
		} else {
			_, clearErr = r.db.DeleteRDPSecurityLogsByCIDR(item.CIDR)
		}
		if clearErr != nil {
			writeError(w, http.StatusInternalServerError, "entry updated but failed to clear connection audit: "+clearErr.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, item)
}

func (r *Router) handleCreateRDPSecurityRule(w http.ResponseWriter, req *http.Request) {
	var rule repository.RDPSecurityRule
	if err := decodeJSON(w, req, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, err := r.db.CreateRDPSecurityRule(rule)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !r.reloadRDPSecurity(w) {
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (r *Router) handleDeleteRDPSecurityRule(w http.ResponseWriter, req *http.Request) {
	if err := r.db.DeleteRDPSecurityRule(req.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "rule not found")
		case errors.Is(err, repository.ErrRDPBuiltinRule):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	if !r.reloadRDPSecurity(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleUpdateRDPSecurityRule merges the request over the stored rule, so
// older clients that only send enabled/threshold/window/ban keep the newer
// fields (scope, aggregation, UDP counting, log clearing) unchanged.
func (r *Router) handleUpdateRDPSecurityRule(w http.ResponseWriter, req *http.Request) {
	current, err := r.db.GetRDPSecurityRule(req.PathValue("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "rule not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rule := *current
	if err := decodeJSON(w, req, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rule.ID = current.ID
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
