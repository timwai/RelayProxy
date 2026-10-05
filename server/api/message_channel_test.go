package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"relayproxy/server/repository"
)

func createMessageDeviceForIdentity(t *testing.T, router *Router, identityID, suffix string) *repository.Device {
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
	if _, err := router.db.SetDeviceIdentity(device.ID, identityID, admin.ID); err != nil {
		t.Fatal(err)
	}
	return device
}

func messageTestIdentity(t *testing.T, router *Router) *repository.Identity {
	t.Helper()
	identities, err := router.db.ListIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) > 0 {
		return identities[0]
	}
	admin, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := router.db.CreateIdentity("Message Tests", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func createMessageTestDevice(t *testing.T, router *Router, suffix string) *repository.Device {
	t.Helper()
	return createMessageDeviceForIdentity(t, router, messageTestIdentity(t, router).ID, suffix)
}

func messageDeviceIdentityID(t *testing.T, router *Router, deviceID string) string {
	t.Helper()
	summary, err := router.db.GetDeviceIdentitySummary(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.IdentityID == "" {
		t.Fatal("message test device has no identity")
	}
	return summary.IdentityID
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

	createBody, _ := json.Marshal(messageChannelRequest{
		ID: "all", IdentityID: messageDeviceIdentityID(t, router, a.ID), Name: "全部", AllDevices: true,
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels", bytes.NewReader(createBody))
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
	device := createMessageTestDevice(t, router, "EMPTY")
	body, _ := json.Marshal(messageChannelRequest{
		ID: "empty", IdentityID: messageDeviceIdentityID(t, router, device.ID), Name: "Empty", AllDevices: false,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels", bytes.NewReader(body))
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

func TestChannelCustomVerificationAndRouting(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	public := NewPublicPushHandler(router.sessions, router.db)
	a := createMessageTestDevice(t, router, "ROUTE-A")
	b := createMessageTestDevice(t, router, "ROUTE-B")

	useDefault := true
	verificationRules := []repository.VerificationRule{{
		Name: "供应商访问密令", Keywords: []string{"访问密令"},
		Pattern: `ID-([A-Z0-9]{4})`, MaxDistance: 64,
	}}
	routeRules := []repository.MessageRouteRule{
		{Name: "系统 A", MatchType: "contains", Pattern: "系统A", DeviceIDs: []string{a.ID}},
		{Name: "系统 B", MatchType: "regex", Pattern: "系统B|业务B", DeviceIDs: []string{b.ID}},
	}
	createBody, _ := json.Marshal(messageChannelRequest{
		ID: "routed", IdentityID: messageDeviceIdentityID(t, router, a.ID), Name: "分流渠道",
		UseDefaultVerification: &useDefault,
		VerificationRules:      &verificationRules,
		RouteRules:             &routeRules,
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels", bytes.NewReader(createBody))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create routed channel returned %d: %s", createRec.Code, createRec.Body.String())
	}

	pushA := httptest.NewRequest(http.MethodGet,
		"/api/v1/push/routed?message="+url.QueryEscape("系统A访问密令 ID-X7P3，请及时处理"), nil)
	recA := httptest.NewRecorder()
	public.ServeHTTP(recA, pushA)
	if recA.Code != http.StatusOK {
		t.Fatalf("route A push returned %d: %s", recA.Code, recA.Body.String())
	}
	var messageA repository.MessageRecord
	if err := json.Unmarshal(recA.Body.Bytes(), &messageA); err != nil {
		t.Fatal(err)
	}
	if messageA.VerificationCode != "X7P3" || messageA.RouteRule != "系统 A" {
		t.Fatalf("unexpected route A message: %+v", messageA)
	}
	if len(messageA.Deliveries) != 1 || messageA.Deliveries[0].DeviceID != a.ID {
		t.Fatalf("route A targets = %+v", messageA.Deliveries)
	}

	pushB := httptest.NewRequest(http.MethodGet,
		"/api/v1/push/routed?message="+url.QueryEscape("业务B通知：您的附加码是 G931"), nil)
	recB := httptest.NewRecorder()
	public.ServeHTTP(recB, pushB)
	if recB.Code != http.StatusOK {
		t.Fatalf("route B push returned %d: %s", recB.Code, recB.Body.String())
	}
	var messageB repository.MessageRecord
	if err := json.Unmarshal(recB.Body.Bytes(), &messageB); err != nil {
		t.Fatal(err)
	}
	if messageB.VerificationCode != "G931" || messageB.RouteRule != "系统 B" {
		t.Fatalf("unexpected route B message: %+v", messageB)
	}
	if len(messageB.Deliveries) != 1 || messageB.Deliveries[0].DeviceID != b.ID {
		t.Fatalf("route B targets = %+v", messageB.Deliveries)
	}

	noMatch := httptest.NewRequest(http.MethodGet,
		"/api/v1/push/routed?message="+url.QueryEscape("系统C普通通知"), nil)
	noMatchRec := httptest.NewRecorder()
	public.ServeHTTP(noMatchRec, noMatch)
	if noMatchRec.Code != http.StatusConflict {
		t.Fatalf("unmatched routed push = %d: %s", noMatchRec.Code, noMatchRec.Body.String())
	}
}

func TestChannelRejectsInvalidCustomRules(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	device := createMessageTestDevice(t, router, "INVALID-RULE")

	useDefault := true
	verificationRules := []repository.VerificationRule{{Name: "bad", Pattern: "("}}
	routeRules := []repository.MessageRouteRule{{
		Name: "bad route", MatchType: "regex", Pattern: "(",
		DeviceIDs: []string{device.ID},
	}}
	for name, body := range map[string]messageChannelRequest{
		"verification": {
			ID: "bad-verification", IdentityID: messageDeviceIdentityID(t, router, device.ID), Name: "bad", DeviceIDs: []string{device.ID},
			UseDefaultVerification: &useDefault, VerificationRules: &verificationRules,
		},
		"routing": {
			ID: "bad-routing", IdentityID: messageDeviceIdentityID(t, router, device.ID), Name: "bad", DeviceIDs: []string{device.ID},
			UseDefaultVerification: &useDefault, RouteRules: &routeRules,
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels", bytes.NewReader(raw))
			req.AddCookie(adminCookie)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestServerMessagesFilterAndClearByChannel(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	public := NewPublicPushHandler(router.sessions, router.db)
	device := createMessageTestDevice(t, router, "CLEAR")

	for _, channel := range []*repository.MessageChannel{
		{ID: "clear-a", Name: "渠道 A", DeviceIDs: []string{device.ID}},
		{ID: "clear-b", Name: "渠道 B", DeviceIDs: []string{device.ID}},
	} {
		if err := router.db.CreateMessageChannel(channel); err != nil {
			t.Fatal(err)
		}
	}

	push := func(channel, message string) repository.MessageRecord {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/push/"+channel+"?message="+url.QueryEscape(message), nil)
		rec := httptest.NewRecorder()
		public.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("push %s returned %d: %s", channel, rec.Code, rec.Body.String())
		}
		var record repository.MessageRecord
		if err := json.Unmarshal(rec.Body.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		return record
	}

	a := push("clear-a", "渠道 A 消息")
	b := push("clear-b", "渠道 B 消息")

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/messages?channelId=clear-a&limit=500", nil)
	listReq.AddCookie(adminCookie)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list channel messages returned %d: %s", listRec.Code, listRec.Body.String())
	}
	var listed []repository.MessageRecord
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != a.ID || listed[0].ChannelID != "clear-a" {
		t.Fatalf("unexpected channel list: %+v", listed)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/messages/"+b.ID, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete message returned %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	_ = push("clear-b", "渠道 B 保留消息")
	clearReq := httptest.NewRequest(http.MethodDelete, "/api/v1/messages?channelId=clear-a", nil)
	clearReq.AddCookie(adminCookie)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear channel returned %d: %s", clearRec.Code, clearRec.Body.String())
	}
	var clearResult struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(clearRec.Body.Bytes(), &clearResult); err != nil {
		t.Fatal(err)
	}
	if clearResult.Deleted != 1 {
		t.Fatalf("cleared %d messages, want 1", clearResult.Deleted)
	}

	allReq := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=500", nil)
	allReq.AddCookie(adminCookie)
	allRec := httptest.NewRecorder()
	router.ServeHTTP(allRec, allReq)
	if allRec.Code != http.StatusOK {
		t.Fatalf("list all messages returned %d: %s", allRec.Code, allRec.Body.String())
	}
	listed = nil
	if err := json.Unmarshal(allRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ChannelID != "clear-b" {
		t.Fatalf("unexpected remaining messages: %+v", listed)
	}

	clearAllReq := httptest.NewRequest(http.MethodDelete, "/api/v1/messages", nil)
	clearAllReq.AddCookie(adminCookie)
	clearAllRec := httptest.NewRecorder()
	router.ServeHTTP(clearAllRec, clearAllReq)
	if clearAllRec.Code != http.StatusOK {
		t.Fatalf("clear all returned %d: %s", clearAllRec.Code, clearAllRec.Body.String())
	}
}


func createMessageIdentityLogin(t *testing.T, router *Router, adminCookie *http.Cookie, username, name string) (repository.Identity, *http.Cookie) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{
		"username": username,
		"name":     name,
		"password": "identity-pass-123",
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities", bytes.NewReader(body))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create identity %s returned %d: %s", name, createRec.Code, createRec.Body.String())
	}
	var identity repository.Identity
	if err := json.Unmarshal(createRec.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}

	loginBody, _ := json.Marshal(map[string]string{"username": username, "password": "identity-pass-123"})
	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login identity %s returned %d: %s", name, loginRec.Code, loginRec.Body.String())
	}
	for _, cookie := range loginRec.Result().Cookies() {
		if cookie.Name == "relay_session_http" {
			return identity, cookie
		}
	}
	t.Fatal("identity session cookie missing")
	return identity, nil
}

func TestMessageChannelsAreIsolatedByIdentity(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	identityA, cookieA := createMessageIdentityLogin(t, router, adminCookie, "message.a", "Message A")
	identityB, cookieB := createMessageIdentityLogin(t, router, adminCookie, "message.b", "Message B")
	deviceA := createMessageDeviceForIdentity(t, router, identityA.ID, "ISO-A")
	deviceB := createMessageDeviceForIdentity(t, router, identityB.ID, "ISO-B")

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels",
		bytes.NewBufferString(`{"id":"identity-a","name":"Identity A","allDevices":true}`))
	createReq.AddCookie(cookieA)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("identity A create channel returned %d: %s", createRec.Code, createRec.Body.String())
	}
	var created repository.MessageChannel
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.IdentityID != identityA.ID {
		t.Fatalf("channel identity = %q, want %q", created.IdentityID, identityA.ID)
	}

	listAReq := httptest.NewRequest(http.MethodGet, "/api/v1/message-channels", nil)
	listAReq.AddCookie(cookieA)
	listARec := httptest.NewRecorder()
	router.ServeHTTP(listARec, listAReq)
	if listARec.Code != http.StatusOK {
		t.Fatalf("identity A list channels returned %d: %s", listARec.Code, listARec.Body.String())
	}
	var channelsA []repository.MessageChannel
	if err := json.Unmarshal(listARec.Body.Bytes(), &channelsA); err != nil {
		t.Fatal(err)
	}
	if len(channelsA) != 1 || channelsA[0].ID != "identity-a" {
		t.Fatalf("identity A channels = %+v", channelsA)
	}

	listBReq := httptest.NewRequest(http.MethodGet, "/api/v1/message-channels", nil)
	listBReq.AddCookie(cookieB)
	listBRec := httptest.NewRecorder()
	router.ServeHTTP(listBRec, listBReq)
	if listBRec.Code != http.StatusOK {
		t.Fatalf("identity B list channels returned %d: %s", listBRec.Code, listBRec.Body.String())
	}
	var channelsB []repository.MessageChannel
	if err := json.Unmarshal(listBRec.Body.Bytes(), &channelsB); err != nil {
		t.Fatal(err)
	}
	if len(channelsB) != 0 {
		t.Fatalf("identity B saw identity A channels: %+v", channelsB)
	}

	updateBReq := httptest.NewRequest(http.MethodPut, "/api/v1/message-channels/identity-a",
		bytes.NewBufferString(`{"name":"stolen","allDevices":true}`))
	updateBReq.AddCookie(cookieB)
	updateBRec := httptest.NewRecorder()
	router.ServeHTTP(updateBRec, updateBReq)
	if updateBRec.Code != http.StatusNotFound {
		t.Fatalf("identity B updated identity A channel: %d %s", updateBRec.Code, updateBRec.Body.String())
	}

	crossReqBody, _ := json.Marshal(messageChannelRequest{
		ID: "cross-device", Name: "Cross", DeviceIDs: []string{deviceB.ID},
	})
	crossReq := httptest.NewRequest(http.MethodPost, "/api/v1/message-channels", bytes.NewReader(crossReqBody))
	crossReq.AddCookie(cookieA)
	crossRec := httptest.NewRecorder()
	router.ServeHTTP(crossRec, crossReq)
	if crossRec.Code != http.StatusBadRequest {
		t.Fatalf("identity A accepted identity B target: %d %s", crossRec.Code, crossRec.Body.String())
	}

	public := NewPublicPushHandler(router.sessions, router.db)
	pushReq := httptest.NewRequest(http.MethodGet, "/api/v1/push/identity-a?message=hello", nil)
	pushRec := httptest.NewRecorder()
	public.ServeHTTP(pushRec, pushReq)
	if pushRec.Code != http.StatusOK {
		t.Fatalf("identity A push returned %d: %s", pushRec.Code, pushRec.Body.String())
	}
	var message repository.MessageRecord
	if err := json.Unmarshal(pushRec.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if message.IdentityID != identityA.ID || len(message.Deliveries) != 1 || message.Deliveries[0].DeviceID != deviceA.ID {
		t.Fatalf("identity A push escaped scope: %+v", message)
	}
	if message.Deliveries[0].DeviceID == deviceB.ID {
		t.Fatal("identity B device received identity A allDevices push")
	}

	messagesAReq := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=500", nil)
	messagesAReq.AddCookie(cookieA)
	messagesARec := httptest.NewRecorder()
	router.ServeHTTP(messagesARec, messagesAReq)
	if messagesARec.Code != http.StatusOK {
		t.Fatalf("identity A messages returned %d: %s", messagesARec.Code, messagesARec.Body.String())
	}
	var messagesA []repository.MessageRecord
	if err := json.Unmarshal(messagesARec.Body.Bytes(), &messagesA); err != nil {
		t.Fatal(err)
	}
	if len(messagesA) != 1 || messagesA[0].IdentityID != identityA.ID {
		t.Fatalf("identity A messages = %+v", messagesA)
	}

	messagesBReq := httptest.NewRequest(http.MethodGet, "/api/v1/messages?limit=500", nil)
	messagesBReq.AddCookie(cookieB)
	messagesBRec := httptest.NewRecorder()
	router.ServeHTTP(messagesBRec, messagesBReq)
	if messagesBRec.Code != http.StatusOK {
		t.Fatalf("identity B messages returned %d: %s", messagesBRec.Code, messagesBRec.Body.String())
	}
	var messagesB []repository.MessageRecord
	if err := json.Unmarshal(messagesBRec.Body.Bytes(), &messagesB); err != nil {
		t.Fatal(err)
	}
	if len(messagesB) != 0 {
		t.Fatalf("identity B saw identity A messages: %+v", messagesB)
	}
}
