package browsersync

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testExtensionID = "abcdefghijklmnopabcdefghijklmnop"

func TestBrowserHandlerRequiresTLSAndAllowlistedExtension(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, []string{testExtensionID})
	for _, tc := range []struct {
		name, origin string
		tls          bool
		want         int
	}{
		{"cleartext", "chrome-extension://" + testExtensionID, false, http.StatusUpgradeRequired},
		{"wrong-extension", "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", true, http.StatusForbidden},
		{"website", "https://example.com", true, http.StatusForbidden},
		{"missing-origin", "", true, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://relay.example.com/api/v1/browser-sync/devices/register", bytes.NewReader([]byte("{}")))
			request.Header.Set("Origin", tc.origin)
			if !tc.tls {
				request.TLS = nil
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("got %d want %d", response.Code, tc.want)
			}
		})
	}
}
func TestBrowserRegisterPendingWithoutPrivilegeEscalation(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, []string{testExtensionID})
	sign, signSpki := makeKey(t)
	_ = sign
	_, encSpki := makeKey(t)
	payload := map[string]any{
		"id":   "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e",
		"name": "Test Chrome", "signingPublicKey": signSpki, "encryptionPublicKey": encSpki,
	}
	call := func() *httptest.ResponseRecorder {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "https://relay.example.com/api/v1/browser-sync/devices/register", bytes.NewReader(body))
		req.Header.Set("Origin", "chrome-extension://"+testExtensionID)
		req.RemoteAddr = "203.0.113.10:2000"
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		return out
	}
	if out := call(); out.Code != http.StatusAccepted {
		t.Fatalf("registration status=%d body=%s", out.Code, out.Body.String())
	}
	d, err := store.Device(httptest.NewRequest("GET", "https://relay.example.com/", nil).Context(), payload["id"].(string))
	if err != nil || d.State != "pending" || d.Send || d.Receive {
		t.Fatalf("pending registration gained privilege: %+v %v", d, err)
	}
	payload["send"] = true
	if out := call(); out.Code != http.StatusForbidden {
		t.Fatalf("privilege escalation returned %d", out.Code)
	}
}
