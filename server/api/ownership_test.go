package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"relayproxy/server/repository"
	"relayproxy/server/service"
	"relayproxy/server/session"
)

func apiUser(t *testing.T, router *Router, name string) (*repository.User, *http.Cookie) {
	t.Helper()
	hash, err := service.HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	user := &repository.User{Username: name, PasswordHash: hash, Role: "user", Status: "active"}
	if err := router.db.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	token, _, err := router.authService.Login(name, "test-password")
	if err != nil {
		t.Fatal(err)
	}
	return user, &http.Cookie{Name: "relay_session", Value: token}
}

func apiRequest(router *Router, cookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestDeviceOwnershipAcrossManagementAndViews(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	owner, ownerCookie := apiUser(t, router, "owner")
	other, _ := apiUser(t, router, "other")
	adminToken, _, err := router.authService.Login("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := &http.Cookie{Name: "relay_session", Value: adminToken}
	seed := func(id, ownerID, mode string) {
		t.Helper()
		if err := router.db.UpsertDevice(&repository.Device{
			ID: id, OwnerUserID: ownerID, Name: id, DeviceMode: mode,
			Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seed("own-client", owner.ID, "CLIENT")
	seed("own-exit", owner.ID, "EXIT")
	seed("other-exit", other.ID, "EXIT")
	for _, pair := range []struct{ id, mode string }{{"own-client", "CLIENT"}, {"own-exit", "EXIT"}, {"other-exit", "EXIT"}} {
		caps := []string{"proxy.client"}
		if pair.mode == "EXIT" {
			caps = []string{"proxy.exit"}
		}
		sess := &session.DeviceSession{DeviceID: pair.id, DeviceName: pair.id, Mode: pair.mode, Grants: caps}
		sess.ActiveStreams.Store(1)
		router.sessions.Register(sess)
	}
	for _, record := range []struct {
		owner string
		bytes int64
	}{{owner.ID, 7}, {other.ID, 9000}} {
		if err := router.db.InsertConnectionAudit(&repository.ConnectionAudit{
			UserID: record.owner, StartedAt: time.Now(), EndedAt: time.Now(), BytesUp: record.bytes, BytesDown: record.bytes,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]int{
		"/api/v1/devices": 2, "/api/v1/exits": 1, "/api/v1/sessions/active": 2,
	} {
		t.Run(path, func(t *testing.T) {
			response := apiRequest(router, ownerCookie, http.MethodGet, path)
			var rows []map[string]any
			if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &rows) != nil || len(rows) != want {
				t.Fatalf("unexpected scoped view: %d %s", response.Code, response.Body.String())
			}
			for _, row := range rows {
				for _, value := range row {
					if value == "other-exit" {
						t.Fatal("another owner's device leaked")
					}
				}
			}
		})
	}
	dashboard := apiRequest(router, ownerCookie, http.MethodGet, "/api/v1/dashboard")
	var stats struct {
		OnlineDevices int
		OnlineExits   int
		TodayUpload   int64
		TodayDownload int64
	}
	if err := json.Unmarshal(dashboard.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if dashboard.Code != http.StatusOK || stats.OnlineDevices != 2 || stats.OnlineExits != 1 || stats.TodayUpload != 7 || stats.TodayDownload != 7 {
		t.Fatalf("dashboard exposes global totals: %s", dashboard.Body.String())
	}

	seed("revoke-target", other.ID, "EXIT")
	if response := apiRequest(router, ownerCookie, http.MethodPost, "/api/v1/devices/revoke-target/revoke"); response.Code != http.StatusForbidden {
		t.Fatalf("ordinary user revoked a device: %d %s", response.Code, response.Body.String())
	}
	if response := apiRequest(router, adminCookie, http.MethodPost, "/api/v1/devices/revoke-target/revoke"); response.Code != http.StatusOK {
		t.Fatalf("admin revoke rejected: %d %s", response.Code, response.Body.String())
	}

	if _, err := router.db.Exec(`UPDATE users SET status = 'disabled' WHERE id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if response := apiRequest(router, ownerCookie, http.MethodGet, "/api/v1/devices"); response.Code != http.StatusUnauthorized {
		t.Fatalf("disabled user retained access: %d", response.Code)
	}
}
