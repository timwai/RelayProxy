package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicRelayHandlerAllowlistAndNoAdminAccess(t *testing.T) {
	browser := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "browser")
		w.WriteHeader(http.StatusAccepted)
	})
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "proxy")
		w.WriteHeader(http.StatusAccepted)
	})
	push := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "push")
		w.WriteHeader(http.StatusAccepted)
	})
	mux := PublicRelayHandler(push, browser, proxy)
	for _, tc := range []struct {
		method, path, handler string
		status                int
	}{
		{"GET", "/api/v1/browser-sync/ws", "browser", 202},
		{"GET", "/api/v1/browser-sync/health", "", http.StatusUpgradeRequired},
		{"POST", "/api/v1/browser-sync/extensions/register", "browser", 202},
		{"POST", "/api/v1/browser-sync/devices/register", "browser", 202},
		{"POST", "/api/v1/push/channel-id", "push", 202},
		{"CONNECT", "example.org:443", "proxy", 202},
		{"GET", "/api/v1/server/config", "", 404},
		{"GET", "/api/v1/browser-sync/admin/devices", "", 404},
		{"GET", "/api/v1/auth/me", "", 404},
		{"POST", "/api/v1/auth/login", "", 404},
		{"GET", "/", "", 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Header().Get("X-Handler") != tc.handler {
			t.Errorf("%s %s returned %d / %q", tc.method, tc.path, rec.Code, rec.Header().Get("X-Handler"))
		}
	}
	// TLS probe succeeds only on this restricted public route and never
	// gives access to the Admin endpoint, even with an approved Browser.
	health := httptest.NewRecorder()
	healthReq := httptest.NewRequest(http.MethodGet, "/api/v1/browser-sync/health", nil)
	healthReq.TLS = &tls.ConnectionState{}
	mux.ServeHTTP(health, healthReq)
	if health.Code != http.StatusNoContent || health.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("TLS Browser Sync health probe failed: %d", health.Code)
	}
	missing := PublicRelayHandler(push, nil, nil)
	notFound := httptest.NewRecorder()
	missing.ServeHTTP(notFound, healthReq)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("disabled Browser Sync leaked endpoint: %d", notFound.Code)
	}
	disabled := PublicRelayHandler(push, browser, nil)
	rec := httptest.NewRecorder()
	disabled.ServeHTTP(rec, httptest.NewRequest(http.MethodConnect, "example.org:443", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unconfigured CONNECT accepted: %d", rec.Code)
	}
}
