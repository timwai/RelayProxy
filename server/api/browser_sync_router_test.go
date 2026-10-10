package api

import (
	"crypto/tls"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	browsersync "relayproxy/server/browser_sync"
	_ "modernc.org/sqlite"
)

func TestBrowserSyncRegistrationIsolatedFromAdminCSRF(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := browsersync.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	const extensionID = "abcdefghijklmnopabcdefghijklmnop"
	router := &Router{mux: http.NewServeMux(),
		browserSync: browsersync.NewHandler(store, []string{extensionID})}
	call := func(path, origin string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "https://relay.example.com"+path, strings.NewReader("{}"))
		req.TLS = &tls.ConnectionState{}
		req.RemoteAddr = "203.0.113.11:10000"
		req.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	// Request reaches the browser registration handler (invalid JSON keys
	// yield 400), rather than being rejected by Admin-only CSRF (403).
	if code := call("/api/v1/browser-sync/devices/register", "chrome-extension://"+extensionID); code != http.StatusBadRequest {
		t.Fatalf("extension registration blocked or routed incorrectly: %d", code)
	}
	// Admin API retains the same-origin CSRF policy.
	if code := call("/api/v1/server/config", "chrome-extension://"+extensionID); code != http.StatusForbidden {
		t.Fatalf("browser extension bypassed Admin CSRF: %d", code)
	}
	if code := call("/api/v1/browser-sync/devices/register", "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); code != http.StatusForbidden {
		t.Fatalf("foreign extension origin accepted: %d", code)
	}
}
