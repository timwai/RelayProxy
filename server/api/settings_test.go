package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayproxy/internal/config"
)

func settingsRouter(t *testing.T) (*Router, *http.Cookie) {
	t.Helper()
	r, cleanup := setupTestRouter(t)
	t.Cleanup(cleanup)
	cfg := &config.ServerConfig{}
	if err := config.NormalizeServerConfig(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := config.SaveServerConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewServerSettings(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	WithServerSettings(settings, nil)(r)
	token, _, err := r.authService.Login("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}
	return r, &http.Cookie{Name: "relay_session_http", Value: token}
}

func TestHTTPIPLoginCookieAndLogout(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()
	srv := httptest.NewServer(r)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", srv.URL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP login failed: %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "relay_session_http" || cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatalf("HTTP cookie cannot be used over an IP connection: %+v", cookies)
	}
	me, err := client.Get(srv.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatal("browser cookie jar did not authenticate subsequent HTTP requests")
	}
	logout, err := client.Post(srv.URL+"/api/v1/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	logout.Body.Close()
	me, err = client.Get(srv.URL + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	me.Body.Close()
	if me.StatusCode != http.StatusUnauthorized {
		t.Fatal("logout did not revoke the HTTP session")
	}
}

func TestHTTPSKeepsSecureSessionCookie(t *testing.T) {
	r, cleanup := setupTestRouter(t)
	defer cleanup()
	srv := httptest.NewTLSServer(r)
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"admin123"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(resp.Cookies()) != 1 || !resp.Cookies()[0].Secure || resp.Cookies()[0].Name != "relay_session" {
		t.Fatal("HTTPS lost its Secure cookie")
	}
}

func TestServerSettingsAuthorizationValidationAndConflict(t *testing.T) {
	r, admin := settingsRouter(t)
	_, user := apiUser(t, r, "settings-reader")
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := apiRequest(r, user, method, "/api/v1/server/config"); rec.Code != http.StatusForbidden {
			t.Fatalf("ordinary user can %s server settings: %d", method, rec.Code)
		}
	}
	rec := apiRequest(r, admin, http.MethodGet, "/api/v1/server/config")
	var initial ServerSettingsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &initial) != nil {
		t.Fatalf("read failed: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "dsn") || strings.Contains(rec.Body.String(), "BEGIN PRIVATE KEY") {
		t.Fatal("private server data leaked")
	}
	submit := func(body any, origin string) *httptest.ResponseRecorder {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPut, "http://192.0.2.10:21080/api/v1/server/config", bytes.NewReader(data))
		req.Header.Set("Origin", origin)
		req.AddCookie(admin)
		response := httptest.NewRecorder()
		r.ServeHTTP(response, req)
		return response
	}
	want := initial.Config
	want.Admin.TLSEnabled = false
	want.Tunnel.HeartbeatSec = 30
	want.RelayACL.AccessMode = "allow"
	want.RelayACL.Domains = []string{" example.org ", "example.org"}
	want.RDPIngress.Enabled = true
	want.RDPIngress.Listen = "127.0.0.1:0"
	want.RDPIngress.PortStart = 34000
	want.RDPIngress.PortEnd = 34100
	want.RDPIngress.SourceCIDRs = []string{" 203.0.113.0/24 ", "203.0.113.0/24"}
	want.RDPIngress.RateLimitPerMin = 240
	request := map[string]any{"revision": initial.Revision, "config": want}
	if rec = submit(request, "http://other.example"); rec.Code != http.StatusForbidden {
		t.Fatal("cross-origin configuration update accepted")
	}
	rec = submit(request, "http://192.0.2.10:21080")
	var saved ServerSettingsResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &saved) != nil {
		t.Fatalf("save failed: %d %s", rec.Code, rec.Body.String())
	}
	if saved.Config.Admin.TLSEnabled || !saved.Config.Tunnel.TLSEnabled || !saved.Runtime.Admin.TLSEnabled || !saved.RestartRequired || len(saved.Config.RelayACL.Domains) != 1 || !saved.Config.RDPIngress.Enabled || saved.Config.RDPIngress.Listen != "127.0.0.1:0" || len(saved.Config.RDPIngress.SourceCIDRs) != 1 {
		t.Fatalf("invalid saved/runtime response: %+v", saved)
	}
	if rec = submit(request, ""); rec.Code != http.StatusConflict {
		t.Fatalf("stale form overwrote saved configuration: %d", rec.Code)
	}
	before, err := os.ReadFile(saved.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	want.Tunnel.MaxConnections = 0
	if rec = submit(map[string]any{"revision": saved.Revision, "config": want}, ""); rec.Code != http.StatusBadRequest {
		t.Fatal("zero limit silently normalized to a default")
	}
	request["revision"] = saved.Revision
	request["unknown"] = true
	if rec = submit(request, ""); rec.Code != http.StatusBadRequest {
		t.Fatal("unknown JSON fields accepted")
	}
	after, _ := os.ReadFile(saved.ConfigPath)
	if !bytes.Equal(before, after) {
		t.Fatal("failed settings request overwrote the file")
	}
	loaded, err := config.LoadServerConfig(saved.ConfigPath)
	if err != nil || loaded.IsAdminTLSEnabled() || !loaded.IsTLSEnabled() || loaded.Tunnel.HeartbeatSec != 30 || loaded.RDP.Ingress.Enabled == nil || !*loaded.RDP.Ingress.Enabled || loaded.RDP.Ingress.Listen != "127.0.0.1:0" || loaded.RDP.Ingress.PortStart != 34000 || loaded.RDP.Ingress.PortEnd != 34100 {
		t.Fatalf("saved configuration did not round-trip: %v", err)
	}
}

func TestRemovedPairEndpointAndStaticAssets(t *testing.T) {
	r, admin := settingsRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/pair-code", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("removed pair endpoint returned %d", rec.Code)
	}
	for _, path := range []string{"/embed.go", "/settings_test.go"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		body, _ := io.ReadAll(rec.Result().Body)
		if rec.Code != http.StatusNotFound || strings.Contains(string(body), "package web") {
			t.Fatalf("source exposed at %s", path)
		}
	}
}
