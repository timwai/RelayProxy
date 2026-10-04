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
	seed("other-client", other.ID, "CLIENT")
	for _, pair := range []struct{ id, mode string }{{"own-client", "CLIENT"}, {"own-exit", "EXIT"}, {"other-client", "CLIENT"}, {"other-exit", "EXIT"}} {
		caps := []string{"proxy.client"}
		if pair.mode == "EXIT" {
			caps = []string{"proxy.exit"}
		}
		sess := &session.DeviceSession{DeviceID: pair.id, DeviceName: pair.id, Mode: pair.mode, Grants: caps}
		sess.ActiveStreams.Store(1)
		router.sessions.Register(sess)
	}
	router.p2pSessions = func() []P2PSessionRuntimeStatus {
		now := time.Now()
		return []P2PSessionRuntimeStatus{
			{SessionID: 1, ClientDeviceID: "own-client", ExitDeviceID: "own-exit", LeaseExpiresAt: now.Add(time.Minute), Answered: true, ClientReport: P2PPeerRuntimeReport{Path: "p2p_quic"}},
			{SessionID: 2, ClientDeviceID: "other-client", ExitDeviceID: "other-exit", LeaseExpiresAt: now.Add(time.Minute), Answered: true, ClientReport: P2PPeerRuntimeReport{Path: "p2p_quic"}},
		}
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
		"/api/v1/devices": 2, "/api/v1/exits": 1, "/api/v1/sessions/active": 2, "/api/v1/p2p/sessions": 1,
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
		OnlineDevices     int
		OnlineExits       int
		TodayUpload       int64
		TodayDownload     int64
		ActiveP2PSessions int
	}
	if err := json.Unmarshal(dashboard.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if dashboard.Code != http.StatusOK || stats.OnlineDevices != 2 || stats.OnlineExits != 1 || stats.TodayUpload != 7 || stats.TodayDownload != 7 || stats.ActiveP2PSessions != 1 {
		t.Fatalf("dashboard exposes global totals: %s", dashboard.Body.String())
	}
	var dashboardFields map[string]json.RawMessage
	if err := json.Unmarshal(dashboard.Body.Bytes(), &dashboardFields); err != nil || dashboardFields["nativeUdp"] != nil {
		t.Fatalf("user dashboard exposes process-wide native UDP telemetry: %s", dashboard.Body.String())
	}
	adminDashboard := apiRequest(router, adminCookie, http.MethodGet, "/api/v1/dashboard")
	if adminDashboard.Code != http.StatusOK || json.Unmarshal(adminDashboard.Body.Bytes(), &dashboardFields) != nil || dashboardFields["nativeUdp"] == nil {
		t.Fatalf("admin dashboard omits native UDP telemetry: %s", adminDashboard.Body.String())
	}
	var udpFields map[string]json.RawMessage
	if err := json.Unmarshal(dashboardFields["nativeUdp"], &udpFields); err != nil || udpFields["queueDrops"] == nil || udpFields["reassemblyDrops"] == nil || udpFields["associationRejects"] == nil {
		t.Fatalf("dashboard native UDP telemetry is incomplete: %s", dashboardFields["nativeUdp"])
	}

	seed("revoke-target", other.ID, "EXIT")
	if response := apiRequest(router, ownerCookie, http.MethodPost, "/api/v1/devices/revoke-target/revoke"); response.Code != http.StatusNotFound {
		t.Fatalf("ordinary user discovered or revoked a device: %d %s", response.Code, response.Body.String())
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
