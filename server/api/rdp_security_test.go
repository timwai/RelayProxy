package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"relayproxy/server/repository"
)

func TestRDPSecurityManagementAPI(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(encoded)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		req.AddCookie(admin)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	ruleRes := request(http.MethodGet, "/api/v1/rdp/security/rules", nil)
	if ruleRes.Code != http.StatusOK {
		t.Fatalf("rules: %d %s", ruleRes.Code, ruleRes.Body.String())
	}
	var rules []repository.RDPSecurityRule
	if err := json.Unmarshal(ruleRes.Body.Bytes(), &rules); err != nil || len(rules) < 2 {
		t.Fatalf("invalid rules: %v %+v", err, rules)
	}
	banRes := request(http.MethodPost, "/api/v1/rdp/security/bans", map[string]any{
		"cidr": "198.51.100.99", "kind": "manual", "reason": "API test", "durationSeconds": 900,
	})
	if banRes.Code != http.StatusCreated {
		t.Fatalf("create ban: %d %s", banRes.Code, banRes.Body.String())
	}
	var ban repository.RDPSecurityBan
	if err := json.Unmarshal(banRes.Body.Bytes(), &ban); err != nil || ban.CIDR != "198.51.100.99/32" {
		t.Fatalf("invalid ban: %+v %v", ban, err)
	}
	if got := request(http.MethodGet, "/api/v1/rdp/security/bans", nil); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(ban.ID)) {
		t.Fatalf("list bans: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodDelete, "/api/v1/rdp/security/bans/"+ban.ID, nil); got.Code != http.StatusOK {
		t.Fatalf("revoke ban: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v1/rdp/security/bans", nil); got.Code != http.StatusOK || bytes.Contains(got.Body.Bytes(), []byte(ban.ID)) {
		t.Fatalf("ban not revoked: %d %s", got.Code, got.Body.String())
	}
	sample := repository.RDPSecurityLog{ID: "api-test", SourceIP: "203.0.113.42", IngressID: "entry", Transport: "tcp", Result: "REJECTED", Reason: "MANUAL_IP_BAN", StartedAt: time.Now().UTC()}
	if err := router.db.InsertRDPSecurityLog(sample); err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/api/v1/rdp/security/logs?ip=203.0.113.42", nil); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte("api-test")) {
		t.Fatalf("list logs: %d %s", got.Code, got.Body.String())
	}
	rules[0].Enabled = false
	if got := request(http.MethodPut, "/api/v1/rdp/security/rules/"+rules[0].ID, rules[0]); got.Code != http.StatusOK {
		t.Fatalf("update rule: %d %s", got.Code, got.Body.String())
	}
	updated, err := router.db.ListRDPSecurityRules()
	if err != nil || len(updated) < 2 || updated[0].Enabled {
		t.Fatalf("rule not updated: %+v %v", updated, err)
	}
}

func TestRDPSecurityGroupedPaginationAndClearAPI(t *testing.T) {
	router, done := setupTestRouter(t)
	defer done()
	admin := loginAdmin(t, router)
	request := func(method, path string, authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		if authorized {
			req.AddCookie(admin)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	now := time.Now().UTC()
	for i, sample := range []repository.RDPSecurityLog{
		{ID: "1", SourceIP: "203.0.113.1", Transport: "tcp", Result: "FORWARDED", StartedAt: now.Add(-time.Minute)},
		{ID: "2", SourceIP: "203.0.113.1", Transport: "tcp", Result: "REJECTED", StartedAt: now},
		{ID: "3", SourceIP: "2001:db8::1", Transport: "udp", Result: "FORWARDED", StartedAt: now},
	} {
		sample.IngressID = "entry"
		sample.TargetDeviceID = "target"
		if err := router.db.InsertRDPSecurityLog(sample); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	groups := request(http.MethodGet, "/api/v1/rdp/security/logs/groups?page=1&pageSize=1", true)
	if groups.Code != http.StatusOK {
		t.Fatalf("grouping: %d %s", groups.Code, groups.Body.String())
	}
	var groupsPage repository.RDPSecurityPage[repository.RDPSourceGroup]
	if err := json.Unmarshal(groups.Body.Bytes(), &groupsPage); err != nil {
		t.Fatal(err)
	}
	if groupsPage.Total != 2 || groupsPage.PageSize != 1 || len(groupsPage.Items) != 1 {
		t.Fatalf("bad grouped pagination: %+v", groupsPage)
	}
	details := request(http.MethodGet, "/api/v1/rdp/security/logs?ip=203.0.113.1&page=2&pageSize=1", true)
	if details.Code != http.StatusOK {
		t.Fatalf("details %d %s", details.Code, details.Body.String())
	}
	var detailPage repository.RDPSecurityPage[repository.RDPSecurityLog]
	if err := json.Unmarshal(details.Body.Bytes(), &detailPage); err != nil {
		t.Fatal(err)
	}
	if detailPage.Total != 2 || detailPage.Page != 2 || len(detailPage.Items) != 1 {
		t.Fatalf("detail paging: %+v", detailPage)
	}
	if got := request(http.MethodDelete, "/api/v1/rdp/security/logs?ip=203.0.113.1", false); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated deletion HTTP %d", got.Code)
	}
	if got := request(http.MethodDelete, "/api/v1/rdp/security/logs?ip=not-an-ip", true); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid IP accepted: %d", got.Code)
	}
	if got := request(http.MethodGet, "/api/v1/rdp/security/logs/groups?page=-1", true); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid page accepted: %d", got.Code)
	}
	if got := request(http.MethodDelete, "/api/v1/rdp/security/logs?ip=203.0.113.1", true); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"deleted":2`)) {
		t.Fatalf("clear one IP: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodDelete, "/api/v1/rdp/security/logs", true); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"deleted":1`)) {
		t.Fatalf("clear all: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/api/v1/rdp/security/logs/groups", true); got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"total":0`)) {
		t.Fatalf("stale groups after clear: %d %s", got.Code, got.Body.String())
	}
}

func TestRDPSecuritySortPermanentAutoClearAndBanFilterPaginationAPI(t *testing.T) {
	router, done := setupTestRouter(t)
	defer done()
	admin := loginAdmin(t, router)
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var reader *bytes.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(encoded)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	now := time.Now().UTC()
	// 198.51.100.10 has 3 older connections; 198.51.100.20 has 1 newer connection.
	for i := 0; i < 3; i++ {
		if err := router.db.InsertRDPSecurityLog(repository.RDPSecurityLog{
			ID: "freq-" + string(rune('a'+i)), IngressID: "entry", TargetDeviceID: "target",
			SourceIP: "198.51.100.10", Transport: "tcp", Result: "REJECTED",
			StartedAt: now.Add(-time.Duration(10-i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := router.db.InsertRDPSecurityLog(repository.RDPSecurityLog{
		ID: "recent-1", IngressID: "entry", TargetDeviceID: "target",
		SourceIP: "198.51.100.20", Transport: "tcp", Result: "FORWARDED",
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Sort by connection count descending puts 198.51.100.10 first.
	sorted := request(http.MethodGet, "/api/v1/rdp/security/logs/groups?sort=connections&order=desc&page=1&pageSize=10", nil)
	if sorted.Code != http.StatusOK {
		t.Fatalf("sort by connections: %d %s", sorted.Code, sorted.Body.String())
	}
	var groups repository.RDPSecurityPage[repository.RDPSourceGroup]
	if err := json.Unmarshal(sorted.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups.Items) != 2 || groups.Items[0].SourceIP != "198.51.100.10" || groups.Items[0].ConnectionCount != 3 {
		t.Fatalf("unexpected sorted order: %+v", groups.Items)
	}

	// One-click permanent ban with auto-clear purges 198.51.100.10's logs.
	permBan := request(http.MethodPost, "/api/v1/rdp/security/bans", map[string]any{
		"cidr": "198.51.100.10", "kind": "manual", "durationSeconds": 0, "reason": "one-click permanent", "clearLogs": true,
	})
	if permBan.Code != http.StatusCreated {
		t.Fatalf("create permanent ban with clearLogs: %d %s", permBan.Code, permBan.Body.String())
	}
	afterClear := request(http.MethodGet, "/api/v1/rdp/security/logs/groups?page=1&pageSize=10", nil)
	if err := json.Unmarshal(afterClear.Body.Bytes(), &groups); err != nil {
		t.Fatal(err)
	}
	if groups.Total != 1 || groups.Items[0].SourceIP != "198.51.100.20" {
		t.Fatalf("expected 198.51.100.10 logs cleared after ban, got %+v", groups)
	}

	// Create a temporary ban for 198.51.100.20 and an allowlist entry, then
	// verify filtered pagination and upgrading the temporary ban to permanent.
	tempRes := request(http.MethodPost, "/api/v1/rdp/security/bans", map[string]any{
		"cidr": "198.51.100.20", "kind": "manual", "durationSeconds": 900, "reason": "temp ban",
	})
	if tempRes.Code != http.StatusCreated {
		t.Fatalf("create temp ban: %d %s", tempRes.Code, tempRes.Body.String())
	}
	var tempBan repository.RDPSecurityBan
	if err := json.Unmarshal(tempRes.Body.Bytes(), &tempBan); err != nil || tempBan.ExpiresAt == nil {
		t.Fatalf("bad temp ban: %+v %v", tempBan, err)
	}
	if allowRes := request(http.MethodPost, "/api/v1/rdp/security/bans", map[string]any{
		"cidr": "192.0.2.1", "kind": "allow", "durationSeconds": 0, "reason": "trusted office",
	}); allowRes.Code != http.StatusCreated {
		t.Fatalf("create allowlist: %d %s", allowRes.Code, allowRes.Body.String())
	}

	tempOnly := request(http.MethodGet, "/api/v1/rdp/security/bans?page=1&pageSize=10&kind=manual&expiry=temporary&search=198.51.100.20", nil)
	if tempOnly.Code != http.StatusOK {
		t.Fatalf("filtered ban page: %d %s", tempOnly.Code, tempOnly.Body.String())
	}
	var banPage repository.RDPSecurityPage[repository.RDPSecurityBan]
	if err := json.Unmarshal(tempOnly.Body.Bytes(), &banPage); err != nil {
		t.Fatal(err)
	}
	if banPage.Total != 1 || len(banPage.Items) != 1 || banPage.Items[0].ID != tempBan.ID {
		t.Fatalf("unexpected filtered ban page: %+v", banPage)
	}

	// Upgrade temporary ban to permanent and auto-clear logs.
	upgrade := request(http.MethodPost, "/api/v1/rdp/security/bans/"+tempBan.ID+"/permanent", map[string]any{"clearLogs": true})
	if upgrade.Code != http.StatusOK {
		t.Fatalf("make ban permanent: %d %s", upgrade.Code, upgrade.Body.String())
	}
	var upgraded repository.RDPSecurityBan
	if err := json.Unmarshal(upgrade.Body.Bytes(), &upgraded); err != nil || upgraded.ExpiresAt != nil || !upgraded.ClearLogs {
		t.Fatalf("expected permanent ban with clearLogs, got %+v (%v)", upgraded, err)
	}
	remainingGroups := request(http.MethodGet, "/api/v1/rdp/security/logs/groups?page=1&pageSize=10", nil)
	if err := json.Unmarshal(remainingGroups.Body.Bytes(), &groups); err != nil || groups.Total != 0 {
		t.Fatalf("expected 198.51.100.20 logs cleared on permanent upgrade, got %+v (%v)", groups, err)
	}

	// Automatic ban rules support permanent ban (banSeconds=0), custom rule creation and deletion.
	createRule := request(http.MethodPost, "/api/v1/rdp/security/rules", map[string]any{
		"name": "自定义低频永久封禁", "enabled": true, "threshold": 5, "windowSeconds": 7200, "banSeconds": 0,
		"scope": "global", "metric": "connections", "countUdp": true, "prefixV4": 24, "prefixV6": 64, "clearLogs": true,
	})
	if createRule.Code != http.StatusCreated {
		t.Fatalf("create custom permanent rule: %d %s", createRule.Code, createRule.Body.String())
	}
	var customRule repository.RDPSecurityRule
	if err := json.Unmarshal(createRule.Body.Bytes(), &customRule); err != nil || customRule.BanSeconds != 0 || !customRule.ClearLogs || customRule.PrefixV4 != 24 {
		t.Fatalf("bad custom rule: %+v (%v)", customRule, err)
	}
	if delBuiltin := request(http.MethodDelete, "/api/v1/rdp/security/rules/persistent_scan", nil); delBuiltin.Code != http.StatusBadRequest {
		t.Fatalf("expected built-in rule delete to fail with 400, got %d", delBuiltin.Code)
	}
	if delCustom := request(http.MethodDelete, "/api/v1/rdp/security/rules/"+customRule.ID, nil); delCustom.Code != http.StatusOK {
		t.Fatalf("delete custom rule: %d %s", delCustom.Code, delCustom.Body.String())
	}
}
