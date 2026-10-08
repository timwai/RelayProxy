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
	router,done:=setupTestRouter(t)
	defer done()
	admin:=loginAdmin(t,router)
	request:=func(method,path string,authorized bool) *httptest.ResponseRecorder {
		t.Helper()
		req:=httptest.NewRequest(method,path,nil)
		if authorized {req.AddCookie(admin)}
		rec:=httptest.NewRecorder()
		router.ServeHTTP(rec,req)
		return rec
	}
	now:=time.Now().UTC()
	for i,sample:=range []repository.RDPSecurityLog{
		{ID:"1",SourceIP:"203.0.113.1",Transport:"tcp",Result:"FORWARDED",StartedAt:now.Add(-time.Minute)},
		{ID:"2",SourceIP:"203.0.113.1",Transport:"tcp",Result:"REJECTED",StartedAt:now},
		{ID:"3",SourceIP:"2001:db8::1",Transport:"udp",Result:"FORWARDED",StartedAt:now},
	} {
		sample.IngressID="entry"
		sample.TargetDeviceID="target"
		if err:=router.db.InsertRDPSecurityLog(sample);err!=nil {t.Fatalf("insert %d: %v",i,err)}
	}
	groups:=request(http.MethodGet,"/api/v1/rdp/security/logs/groups?page=1&pageSize=1",true)
	if groups.Code!=http.StatusOK {t.Fatalf("grouping: %d %s",groups.Code,groups.Body.String())}
	var groupsPage repository.RDPSecurityPage[repository.RDPSourceGroup]
	if err:=json.Unmarshal(groups.Body.Bytes(),&groupsPage);err!=nil {t.Fatal(err)}
	if groupsPage.Total!=2 || groupsPage.PageSize!=1 || len(groupsPage.Items)!=1 {
		t.Fatalf("bad grouped pagination: %+v",groupsPage)
	}
	details:=request(http.MethodGet,"/api/v1/rdp/security/logs?ip=203.0.113.1&page=2&pageSize=1",true)
	if details.Code!=http.StatusOK {t.Fatalf("details %d %s",details.Code,details.Body.String())}
	var detailPage repository.RDPSecurityPage[repository.RDPSecurityLog]
	if err:=json.Unmarshal(details.Body.Bytes(),&detailPage);err!=nil {t.Fatal(err)}
	if detailPage.Total!=2 || detailPage.Page!=2 || len(detailPage.Items)!=1 {t.Fatalf("detail paging: %+v",detailPage)}
	if got:=request(http.MethodDelete,"/api/v1/rdp/security/logs?ip=203.0.113.1",false);got.Code!=http.StatusUnauthorized {
		t.Fatalf("unauthenticated deletion HTTP %d",got.Code)
	}
	if got:=request(http.MethodDelete,"/api/v1/rdp/security/logs?ip=not-an-ip",true);got.Code!=http.StatusBadRequest {
		t.Fatalf("invalid IP accepted: %d",got.Code)
	}
	if got:=request(http.MethodGet,"/api/v1/rdp/security/logs/groups?page=-1",true);got.Code!=http.StatusBadRequest {
		t.Fatalf("invalid page accepted: %d",got.Code)
	}
	if got:=request(http.MethodDelete,"/api/v1/rdp/security/logs?ip=203.0.113.1",true);got.Code!=http.StatusOK || !bytes.Contains(got.Body.Bytes(),[]byte(`"deleted":2`)) {
		t.Fatalf("clear one IP: %d %s",got.Code,got.Body.String())
	}
	if got:=request(http.MethodDelete,"/api/v1/rdp/security/logs",true);got.Code!=http.StatusOK || !bytes.Contains(got.Body.Bytes(),[]byte(`"deleted":1`)) {
		t.Fatalf("clear all: %d %s",got.Code,got.Body.String())
	}
	if got:=request(http.MethodGet,"/api/v1/rdp/security/logs/groups",true);got.Code!=http.StatusOK || !bytes.Contains(got.Body.Bytes(),[]byte(`"total":0`)) {
		t.Fatalf("stale groups after clear: %d %s",got.Code,got.Body.String())
	}
}
