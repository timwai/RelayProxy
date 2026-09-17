package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"relayproxy/server/repository"
	"relayproxy/server/session"
)

func passwordBody(t *testing.T, current, next string) string {
	t.Helper()
	data, err := json.Marshal(map[string]string{"currentPassword": current, "newPassword": next})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func changePasswordRequest(router *Router, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", strings.NewReader(body))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPasswordChangeAcrossHTTPAndHTTPS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "HTTP"
		if secure {
			name = "HTTPS"
		}
		t.Run(name, func(t *testing.T) {
			router, cleanup := setupTestRouter(t)
			defer cleanup()
			var srv *httptest.Server
			if secure {
				srv = httptest.NewTLSServer(router)
			} else {
				srv = httptest.NewServer(router)
			}
			defer srv.Close()
			newClient := func() *http.Client {
				client := *srv.Client()
				client.Jar, _ = cookiejar.New(nil)
				return &client
			}
			request := func(client *http.Client, method, path, body string) (*http.Response, []byte) {
				t.Helper()
				req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Origin", srv.URL)
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				return resp, data
			}
			first, second := newClient(), newClient()
			for _, client := range []*http.Client{first, second} {
				resp, data := request(client, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"admin123"}`)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("login failed: %d %s", resp.StatusCode, data)
				}
			}
			admin, err := router.db.GetUserByUsername("admin")
			if err != nil {
				t.Fatal(err)
			}
			if err := router.db.UpsertDevice(&repository.Device{ID: "live-device", OwnerUserID: admin.ID, Name: "live", DeviceMode: "CLIENT", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			router.sessions.Register(&session.DeviceSession{DeviceID: "live-device", Mode: "CLIENT"})
			_, otherCookie := apiUser(t, router, "unaffected-user")
			otherCookie.Name = sessionCookieName(httptest.NewRequest(http.MethodGet, srv.URL, nil))
			// Wrong current passwords are form errors and keep the browser signed in.
			resp, _ := request(first, http.MethodPut, "/api/v1/auth/password", passwordBody(t, "wrong", "new-admin-password"))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("wrong current password status: %d", resp.StatusCode)
			}
			if resp, _ = request(first, http.MethodGet, "/api/v1/auth/me", ""); resp.StatusCode != http.StatusOK {
				t.Fatal("incorrect password forced logout")
			}
			resp, data := request(first, http.MethodPut, "/api/v1/auth/password", passwordBody(t, "admin123", "new-admin-password"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("password change failed: %d %s", resp.StatusCode, data)
			}
			if resp.Header.Get("Cache-Control") != "no-store" || bytes.Contains(data, []byte("admin123")) || bytes.Contains(data, []byte("new-admin-password")) || bytes.Contains(data, []byte("$argon2id$")) {
				t.Fatal("password response exposed credentials or allowed caching")
			}
			cookies := resp.Cookies()
			if len(cookies) != 2 {
				t.Fatalf("expected to clear both cookie names, got %d", len(cookies))
			}
			for _, cookie := range cookies {
				if cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.HttpOnly || cookie.Secure != secure {
					t.Fatal("password change did not clear session cookies correctly")
				}
			}
			for _, client := range []*http.Client{first, second} {
				if resp, _ = request(client, http.MethodGet, "/api/v1/auth/me", ""); resp.StatusCode != http.StatusUnauthorized {
					t.Fatal("an old browser session still authenticates")
				}
			}
			otherReq := httptest.NewRequest(http.MethodGet, srv.URL+"/api/v1/auth/me", nil)
			otherReq.AddCookie(otherCookie)
			otherRec := httptest.NewRecorder()
			router.ServeHTTP(otherRec, otherReq)
			if otherRec.Code != http.StatusOK {
				t.Fatal("password change affected a different user's session")
			}
			devices, err := router.db.ListDevices()
			if err != nil || len(devices) != 1 || devices[0].ApprovalState != repository.EnrollmentApproved {
				t.Fatal("password change modified device approval")
			}
			if _, ok := router.sessions.Get("live-device"); !ok {
				t.Fatal("password change disconnected a tunnel")
			}
			if resp, _ = request(first, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"admin123"}`); resp.StatusCode != http.StatusUnauthorized {
				t.Fatal("old password still logs in")
			}
			if resp, _ = request(first, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"new-admin-password"}`); resp.StatusCode != http.StatusOK {
				t.Fatal("new password cannot log in")
			}
		})
	}
}

func TestPasswordAPIValidatesIdentityOriginAndBody(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	token, original, err := router.authService.Login("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "relay_session_http", Value: token}
	valid := passwordBody(t, "admin123", "new-admin-password")
	if rec := changePasswordRequest(router, nil, valid); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request accepted: %d", rec.Code)
	}
	for _, test := range []struct{ origin, site string }{{"http://untrusted.example", ""}, {"", "cross-site"}} {
		req := httptest.NewRequest(http.MethodPut, "http://192.0.2.10:21080/api/v1/auth/password", strings.NewReader(valid))
		req.AddCookie(cookie)
		req.Header.Set("Origin", test.origin)
		req.Header.Set("Sec-Fetch-Site", test.site)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cross-origin request accepted: %d", rec.Code)
		}
	}
	for name, body := range map[string]string{
		"missing":    `{}`,
		"null":       `null`,
		"array":      `[]`,
		"broken":     `{"currentPassword":`,
		"trailing":   valid + `{}`,
		"other-user": `{"currentPassword":"admin123","newPassword":"new-admin-password","userId":"other"}`,
		"short":      passwordBody(t, "admin123", "short"),
		"same":       passwordBody(t, "admin123", "admin123"),
		"oversized":  passwordBody(t, "admin123", strings.Repeat("a", 129<<10)),
	} {
		t.Run(name, func(t *testing.T) {
			if rec := changePasswordRequest(router, cookie, body); rec.Code != http.StatusBadRequest {
				t.Fatalf("invalid request accepted: %d", rec.Code)
			}
		})
	}
	stored, err := router.db.GetUserByUsername("admin")
	if err != nil || stored.PasswordHash != original.PasswordHash {
		t.Fatal("rejected request changed credentials")
	}
	if _, ok := router.authService.Authenticate(token); !ok {
		t.Fatal("rejected request revoked the session")
	}
	// Ordinary management users can change their own password, with no target
	// account ID or administrator-only configuration permission involved.
	_, ownCookie := apiUser(t, router, "self-service-user")
	if rec := changePasswordRequest(router, ownCookie, passwordBody(t, "test-password", "new-user-password")); rec.Code != http.StatusOK {
		t.Fatalf("self-service password change failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := router.authService.Authenticate(token); !ok {
		t.Fatal("another user's change revoked the administrator")
	}
}

func TestPasswordAPIRateLimitAndStorageFailure(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	token, _, err := router.authService.Login("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "relay_session_http", Value: token}
	if _, err := router.db.Exec(`CREATE TRIGGER fail_password BEFORE UPDATE OF password_hash ON users
		BEGIN SELECT RAISE(ABORT, 'private database failure details'); END`); err != nil {
		t.Fatal(err)
	}
	rec := changePasswordRequest(router, cookie, passwordBody(t, "admin123", "new-admin-password"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "private database") || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("storage error was not handled safely: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := router.db.Exec(`DROP TRIGGER fail_password`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		if rec = changePasswordRequest(router, cookie, passwordBody(t, "wrong", "new-admin-password")); rec.Code != http.StatusBadRequest {
			t.Fatalf("wrong password attempt status: %d", rec.Code)
		}
	}
	rec = changePasswordRequest(router, cookie, passwordBody(t, "admin123", "new-admin-password"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatal("password guesses were not rate limited")
	}
	if rec := apiRequest(router, cookie, http.MethodGet, "/api/v1/auth/me"); rec.Code != http.StatusOK {
		t.Fatal("password rate limiting affected other management operations")
	}
}
