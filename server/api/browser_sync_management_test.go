package api

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	browsersync "relayproxy/server/browser_sync"
)

func testBrowserAdminRouter(t *testing.T) (*Router, *browsersync.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE identities(id TEXT PRIMARY KEY,status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO identities(id,status) VALUES('id_1','active')`); err != nil {
		t.Fatal(err)
	}
	store, err := browsersync.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return &Router{browserSync: browsersync.NewHandler(store, nil)}, store
}

func TestBrowserAdminRulePaginationAPIIsValidatedAndLegacyListCompatible(t *testing.T) {
	router, _ := testBrowserAdminRouter(t)
	call := func(path string) (int, any) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		router.handleBrowserSyncRules(w, req)
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid JSON from %s: %v", path, err)
		}
		return w.Code, body
	}
	if code, body := call("/api/v1/browser-sync/admin/rules"); code != http.StatusOK {
		t.Fatalf("legacy rule response failed: %d %v", code, body)
	} else if _, ok := body.([]any); !ok {
		t.Fatalf("legacy rule list shape changed: %T", body)
	}
	if code, body := call("/api/v1/browser-sync/admin/rules?limit=50&offset=0"); code != http.StatusOK {
		t.Fatalf("paged rule response failed: %d %v", code, body)
	} else if obj, ok := body.(map[string]any); !ok || obj["nextOffset"] != float64(-1) {
		t.Fatalf("paged rule response shape: %#v", body)
	}
	for _, path := range []string{
		"/api/v1/browser-sync/admin/rules?limit=101",
		"/api/v1/browser-sync/admin/rules?limit=0",
		"/api/v1/browser-sync/admin/rules?offset=-1",
		"/api/v1/browser-sync/admin/rules?status=invalid",
	} {
		if code, _ := call(path); code != http.StatusBadRequest {
			t.Fatalf("invalid rule filter accepted: %s -> %d", path, code)
		}
	}
}

func TestBrowserAdminAuditAPIRejectsInvalidFiltersAndRequiresLogin(t *testing.T) {
	router, _ := testBrowserAdminRouter(t)
	call := func(path string) int {
		t.Helper()
		w := httptest.NewRecorder()
		router.handleBrowserSyncAudit(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w.Code
	}
	for _, path := range []string{
		"/api/v1/browser-sync/admin/audit?before=-1",
		"/api/v1/browser-sync/admin/audit?limit=101",
		"/api/v1/browser-sync/admin/audit?type=cookies",
		"/api/v1/browser-sync/admin/audit?device=untrusted",
	} {
		if got := call(path); got != http.StatusBadRequest {
			t.Fatalf("invalid audit filter accepted: %s -> %d", path, got)
		}
	}
	handler := router.requireAuth(router.requireAdmin(router.handleBrowserSyncAudit))
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/browser-sync/admin/audit", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous browser admin audit granted access: %d", w.Code)
	}
}

func TestBrowserAdminActionGetsActorFromAuthenticatedContextOnly(t *testing.T) {
	router, store := testBrowserAdminRouter(t)
	ctx := context.Background()
	deviceID := "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e"
	if _, err := store.Register(ctx, browsersync.BrowserDevice{
		ID: deviceID, Name: "Browser actor test",
		SigningKey: "fixture-key-a", EncryptionKey: "fixture-key-b",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(ctx, deviceID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/browser-sync/admin/devices/"+deviceID+"/revoke", nil)
	req.SetPathValue("id", deviceID)
	req = req.WithContext(context.WithValue(req.Context(), userContextKey, "user-admin-1"))
	w := httptest.NewRecorder()
	router.handleBrowserSyncRevoke(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("device revoke failed: %d %s", w.Code, w.Body.String())
	}
	events, err := store.AdminAudit(ctx, 0, 10)
	if err != nil || len(events) < 2 || events[0].Type != "device_revoked" ||
		events[0].ActorID != "user-admin-1" {
		t.Fatalf("admin actor not attached to mutation audit: %+v %v", events, err)
	}
}

func TestBrowserAdminAuditCSVExportAndClearRequireActor(t *testing.T) {
	router, store := testBrowserAdminRouter(t)
	ctx := context.Background()
	deviceID := "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e"
	if _, err := store.Register(ctx, browsersync.BrowserDevice{
		ID: deviceID, Name: "Audit export",
		SigningKey: "test-signing-public", EncryptionKey: "test-encryption-public",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(browsersync.WithAdminActor(ctx, "admin-export"), deviceID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/browser-sync/admin/audit/export", nil)
	w := httptest.NewRecorder()
	router.handleBrowserSyncAuditExport(w, req)
	if w.Code != http.StatusOK ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe audit CSV response: %d %v", w.Code, w.Header())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(records) != 2 ||
		records[1][3] != "admin-export" || records[1][2] != "device_approved" {
		t.Fatalf("invalid audit export: %+v %v", records, err)
	}
	if got := browserAuditCSVCell("=HYPERLINK(1,2)"); got != "'=HYPERLINK(1,2)" {
		t.Fatalf("CSV formula injection was not neutralized: %q", got)
	}
	clearReq := httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sync/admin/audit", nil)
	clearW := httptest.NewRecorder()
	router.handleBrowserSyncAuditClear(clearW, clearReq)
	if clearW.Code != http.StatusForbidden {
		t.Fatalf("audit cleared without a validated admin actor: %d", clearW.Code)
	}
	clearReq = clearReq.WithContext(context.WithValue(clearReq.Context(), userContextKey, "admin-export"))
	clearW = httptest.NewRecorder()
	router.handleBrowserSyncAuditClear(clearW, clearReq)
	if clearW.Code != http.StatusOK {
		t.Fatalf("admin audit clear failed: %d %s", clearW.Code, clearW.Body.String())
	}
	events, err := store.AdminAudit(ctx, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "audit_cleared" ||
		events[0].ActorID != "admin-export" {
		t.Fatalf("audit clear had no immutable actor trail: %+v %v", events, err)
	}
}
