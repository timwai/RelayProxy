package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"relayproxy/server/repository"
)

func TestMessageWebhookPersistsOfflineDeliveryAndVerificationCode(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	router.webhookToken = "test-webhook-token"

	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "message-webhook-fingerprint", InstallationID: "message-webhook-install",
		PublicKey: []byte("message-webhook-key"), DeviceName: "Message-PC",
		Platform: "windows", Arch: "amd64", RequestedCapabilities: []string{"proxy.client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{
		"deviceIds": []string{device.ID, device.ID},
		"title":     "登录验证码",
		"message":   "您的登录验证码为 482931，5 分钟内有效。",
		"source":    "test-suite",
	})

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/messages", bytes.NewReader(payload))
	unauthorized.Header.Set("Authorization", "Bearer wrong-token")
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token returned %d: %s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/messages", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer test-webhook-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("webhook returned %d: %s", response.Code, response.Body.String())
	}

	var message repository.MessageRecord
	if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
		t.Fatalf("decode webhook response: %v", err)
	}
	if message.VerificationCode != "482931" {
		t.Fatalf("verification code = %q, want 482931", message.VerificationCode)
	}
	if len(message.Deliveries) != 1 {
		t.Fatalf("deliveries = %d, want deduplicated single target", len(message.Deliveries))
	}
	if message.Deliveries[0].Status != "offline" {
		t.Fatalf("delivery status = %q, want offline", message.Deliveries[0].Status)
	}

	adminCookie := loginAdmin(t, router)
	historyRequest := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=10", nil)
	historyRequest.AddCookie(adminCookie)
	historyResponse := httptest.NewRecorder()
	router.ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK {
		t.Fatalf("history returned %d: %s", historyResponse.Code, historyResponse.Body.String())
	}
	var history []*repository.MessageRecord
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(history) != 1 || history[0].ID != message.ID {
		t.Fatalf("unexpected message history: %+v", history)
	}
	if len(history[0].Deliveries) != 1 || history[0].Deliveries[0].Status != "offline" {
		t.Fatalf("unexpected delivery history: %+v", history[0].Deliveries)
	}
}

func TestMessageWebhookDisabledWithoutToken(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/messages", bytes.NewReader([]byte(`{"deviceId":"device","message":"test"}`)))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled webhook returned %d: %s", response.Code, response.Body.String())
	}
}
