package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"relayproxy/server/repository"
)

func createMessageTestDevice(t *testing.T, router *Router, suffix string) *repository.Device {
	t.Helper()
	admin, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint:           "msg-fp-" + suffix,
		InstallationID:        "msg-install-" + suffix,
		PublicKey:             []byte("msg-key-" + suffix),
		DeviceName:            "Message-" + suffix,
		Platform:              "windows",
		Arch:                  "amd64",
		RequestedCapabilities: []string{"proxy.client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, admin.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func TestChannelPushGETAndPOST(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()

	device := createMessageTestDevice(t, router, "A")
	public := NewPublicPushHandler(router.sessions, router.db)
	channel := &repository.MessageChannel{
		ID:        "login-code",
		Name:      "登录验证码",
		DeviceIDs: []string{device.ID},
	}
	if err := router.db.CreateMessageChannel(channel); err != nil {
		t.Fatal(err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/push/login-code?message=验证码%20482931", nil)
	getRec := httptest.NewRecorder()
	public.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET push returned %d: %s", getRec.Code, getRec.Body.String())
	}
	var got repository.MessageRecord
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ChannelID != "login-code" || got.VerificationCode != "482931" {
		t.Fatalf("unexpected GET message: %+v", got)
	}
	if len(got.Deliveries) != 1 || got.Deliveries[0].DeviceID != device.ID {
		t.Fatalf("unexpected GET deliveries: %+v", got.Deliveries)
	}

	body, _ := json.Marshal(map[string]string{"title": "通知", "message": "维护开始"})
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/push/login-code", bytes.NewReader(body))
	postRec := httptest.NewRecorder()
	public.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST push returned %d: %s", postRec.Code, postRec.Body.String())
	}
}

func TestChannelAllDevicesAndCRUD(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	public := NewPublicPushHandler(router.sessions, router.db)
	a := createMessageTestDevice(t, router, "ALL-A")
	b := createMessageTestDevice(t, router, "ALL-B")

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels",
		bytes.NewBufferString(`{"id":"all","name":"全部","allDevices":true}`))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create channel returned %d: %s", createRec.Code, createRec.Body.String())
	}

	pushReq := httptest.NewRequest(http.MethodGet, "/api/v1/push/all?message=hello", nil)
	pushRec := httptest.NewRecorder()
	public.ServeHTTP(pushRec, pushReq)
	if pushRec.Code != http.StatusOK {
		t.Fatalf("push returned %d: %s", pushRec.Code, pushRec.Body.String())
	}
	var message repository.MessageRecord
	if err := json.Unmarshal(pushRec.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, delivery := range message.Deliveries {
		targets[delivery.DeviceID] = true
	}
	if len(targets) != 2 || !targets[a.ID] || !targets[b.ID] {
		t.Fatalf("unexpected all-device targets: %+v", targets)
	}

	updateBody, _ := json.Marshal(map[string]any{
		"name":       "仅 A",
		"allDevices": false,
		"deviceIds":  []string{a.ID},
	})
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/message-channels/all", bytes.NewReader(updateBody))
	updateReq.AddCookie(adminCookie)
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update channel returned %d: %s", updateRec.Code, updateRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/message-channels/all", nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete channel returned %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestChannelRequiresTargets(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels",
		bytes.NewBufferString(`{"id":"empty","name":"Empty","allDevices":false}`))
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}


func TestAdminListenerDoesNotServePublicPush(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/push/missing?message=hello", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("admin push route returned %d: %s", response.Code, response.Body.String())
	}
}
