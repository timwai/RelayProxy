package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/service"
	"relayproxy/server/session"
)

func setupTestRouter(t *testing.T) (*Router, func()) {
	t.Helper()
	t.Setenv("RELAY_ADMIN_PASSWORD", "admin123")
	db, err := repository.OpenDB("sqlite", filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	router := NewRouter(service.NewAuthService(db), service.NewDeviceService(db), session.NewManager(), db)
	return router, func() { _ = db.Close() }
}

func loginAdmin(t *testing.T, router *Router) *http.Cookie {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "admin123"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "relay_session_http" {
			return cookie
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func TestServerExitAppearsInExitAndDashboardAPIs(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	WithServerExitStatus(func() ServerExitRuntimeStatus {
		return ServerExitRuntimeStatus{Enabled: true, ActiveStreams: 3}
	})(router)
	admin := loginAdmin(t, router)

	exitsReq := httptest.NewRequest(http.MethodGet, "/api/v1/exits", nil)
	exitsReq.AddCookie(admin)
	exitsRec := httptest.NewRecorder()
	router.ServeHTTP(exitsRec, exitsReq)
	if exitsRec.Code != http.StatusOK {
		t.Fatalf("list exits failed: %d %s", exitsRec.Code, exitsRec.Body.String())
	}
	var exits []map[string]any
	if err := json.Unmarshal(exitsRec.Body.Bytes(), &exits); err != nil {
		t.Fatal(err)
	}
	if len(exits) != 1 || exits[0]["deviceId"] != protocol.ServerExitDeviceID || exits[0]["transport"] != "local" || exits[0]["activeStreams"] != float64(3) {
		t.Fatalf("server exit missing from API: %+v", exits)
	}

	dashboardReq := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)
	dashboardReq.AddCookie(admin)
	dashboardRec := httptest.NewRecorder()
	router.ServeHTTP(dashboardRec, dashboardReq)
	if dashboardRec.Code != http.StatusOK {
		t.Fatalf("dashboard failed: %d %s", dashboardRec.Code, dashboardRec.Body.String())
	}
	var dashboard map[string]any
	if err := json.Unmarshal(dashboardRec.Body.Bytes(), &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard["onlineExits"] != float64(1) {
		t.Fatalf("dashboard server exit count=%v, want 1", dashboard["onlineExits"])
	}
}

func TestEnrollmentApprovalAPI(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "fingerprint", InstallationID: "installation", PublicKey: []byte("public-key"),
		DeviceName: "Test-PC", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"proxy.client"},
	})
	if err != nil {
		t.Fatal(err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/enrollments?state=pending", nil)
	listReq.AddCookie(admin)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !bytes.Contains(listRec.Body.Bytes(), []byte(pending.RequestID)) {
		t.Fatalf("pending enrollment not listed: %d %s", listRec.Code, listRec.Body.String())
	}

	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/"+pending.RequestID+"/approve", bytes.NewReader([]byte(`{}`)))
	approveReq.AddCookie(admin)
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusOK || !bytes.Contains(approveRec.Body.Bytes(), []byte(`"approvalState":"approved"`)) {
		t.Fatalf("approval failed: %d %s", approveRec.Code, approveRec.Body.String())
	}
	var approved repository.Device
	if err := json.Unmarshal(approveRec.Body.Bytes(), &approved); err != nil || approved.ID == "" {
		t.Fatalf("invalid approval response: %+v err=%v", approved, err)
	}
	router.sessions.Register(&session.DeviceSession{DeviceID: approved.ID})
	revokeReq := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+approved.ID+"/revoke", bytes.NewReader([]byte(`{"reason":"test"}`)))
	revokeReq.AddCookie(admin)
	revokeRec := httptest.NewRecorder()
	router.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke failed: %d %s", revokeRec.Code, revokeRec.Body.String())
	}
	if _, online := router.sessions.Get(approved.ID); online {
		t.Fatal("revoked device session remained registered")
	}

	for _, path := range []string{"/api/v1/devices/pair-code", "/api/v1/device/pair", "/api/v1/devices/unknown/rotate-token"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.AddCookie(admin)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("removed credential endpoint %s returned %d", path, rec.Code)
		}
	}
}

func TestIncrementalEnrollmentApprovalInvalidatesExistingSession(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := router.db.CreateIdentity("Android", adminUser.ID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := router.db.ResolveIdentity(identity.ShortID)
	if err != nil {
		t.Fatal(err)
	}
	observation := repository.DeviceIdentityObservation{
		Fingerprint: "incremental-api-fingerprint", InstallationID: "incremental-api-install", PublicKey: []byte("incremental-api-key"),
		DeviceName: "Android", Platform: "android", Arch: "arm64", RequestedCapabilities: []string{"proxy.exit"},
	}
	pending, err := router.db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollmentForIdentity(pending.RequestID, adminUser.ID, identity.ID, []string{"proxy.exit"})
	if err != nil {
		t.Fatal(err)
	}
	observation.RequestedCapabilities = []string{"proxy.client", "proxy.exit"}
	incremental, err := router.db.ObserveIdentityDevice(*resolved, observation)
	if err != nil {
		t.Fatal(err)
	}
	router.sessions.Register(&session.DeviceSession{DeviceID: device.ID})

	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/"+incremental.RequestID+"/approve",
		bytes.NewReader([]byte(`{"capabilities":["proxy.client"]}`)))
	approveReq.AddCookie(adminCookie)
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("incremental approval failed: %d %s", approveRec.Code, approveRec.Body.String())
	}
	var updated repository.Device
	if err := json.Unmarshal(approveRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != device.ID || len(updated.ApprovedCapabilities) != 2 {
		t.Fatalf("incremental approval did not merge capabilities: %+v", updated)
	}
	if _, online := router.sessions.Get(device.ID); online {
		t.Fatal("incremental approval left the stale session registered")
	}
}

func TestDeleteDeviceAPI(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	observation := repository.DeviceIdentityObservation{
		Fingerprint: "delete-api-fingerprint", InstallationID: "delete-api-install", PublicKey: []byte("delete-api-key"),
		DeviceName: "Delete API", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"proxy.client"},
	}
	pending, err := router.db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}
	router.sessions.Register(&session.DeviceSession{DeviceID: device.ID})

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/devices/"+device.ID, nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK || !bytes.Contains(deleteRec.Body.Bytes(), []byte(`"state":"deleted"`)) {
		t.Fatalf("delete failed: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, online := router.sessions.Get(device.ID); online {
		t.Fatal("deleted device session remained registered")
	}
	devices, err := router.db.ListDevices()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range devices {
		if item.ID == device.ID {
			t.Fatalf("deleted device still listed: %+v", item)
		}
	}

	decision, err := router.db.ObserveDeviceIdentity(observation)
	if err != nil {
		t.Fatal(err)
	}
	if decision.State != repository.EnrollmentPending || decision.RequestID == "" {
		t.Fatalf("deleted device did not return as pending enrollment: %+v", decision)
	}
}

func TestUpdateDeviceCapabilitiesAPI(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	requested := []string{"proxy.client", "proxy.exit", "rdp.controller", "rdp.host", "rdp.public"}
	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "capability-api-fingerprint", InstallationID: "capability-api-install", PublicKey: []byte("capability-api-key"),
		DeviceName: "Capability API", Platform: "windows", Arch: "amd64", RequestedCapabilities: requested,
	})
	if err != nil {
		t.Fatal(err)
	}
	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/enrollments/"+pending.RequestID+"/approve", bytes.NewReader([]byte(`{"capabilities":["proxy.client"]}`)))
	approveReq.AddCookie(admin)
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("approval failed: %d %s", approveRec.Code, approveRec.Body.String())
	}
	var approved repository.Device
	if err := json.Unmarshal(approveRec.Body.Bytes(), &approved); err != nil || approved.ID == "" {
		t.Fatalf("invalid approval response: %+v err=%v", approved, err)
	}
	router.sessions.Register(&session.DeviceSession{DeviceID: approved.ID})

	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/devices/"+approved.ID+"/capabilities", bytes.NewReader([]byte(`{"capabilities":["proxy.client","proxy.exit","rdp.controller","rdp.host","rdp.public"]}`)))
	updateReq.AddCookie(admin)
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("capability update failed: %d %s", updateRec.Code, updateRec.Body.String())
	}
	var updated repository.Device
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil || len(updated.ApprovedCapabilities) != len(requested) {
		t.Fatalf("invalid capability update response: %+v err=%v", updated, err)
	}
	if _, online := router.sessions.Get(approved.ID); online {
		t.Fatal("capability update left the old session registered")
	}
}

func TestRDPExplicitTargetGrantAPI(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	approve := func(fingerprint, name string, capabilities []string) *repository.Device {
		t.Helper()
		pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
			Fingerprint: fingerprint, InstallationID: fingerprint + "-install", PublicKey: []byte(fingerprint + "-key"),
			DeviceName: name, Platform: "windows", Arch: "amd64", RequestedCapabilities: capabilities,
		})
		if err != nil {
			t.Fatal(err)
		}
		device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, capabilities)
		if err != nil {
			t.Fatal(err)
		}
		return device
	}
	controller := approve("rdp-controller-api", "Controller", []string{"rdp.controller"})
	first := approve("rdp-target-a-api", "Target A", []string{"rdp.host"})
	second := approve("rdp-target-b-api", "Target B", []string{"rdp.host"})

	getTargets := func() []repository.RDPTarget {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/rdp/targets?controllerId="+controller.ID, nil)
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list RDP targets failed: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Targets []repository.RDPTarget `json:"targets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Targets
	}
	if targets := getTargets(); len(targets) != 0 {
		t.Fatalf("same-owner RDP hosts were visible before explicit grant: %+v", targets)
	}

	put := func(ids []string) []repository.RDPTarget {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"targetDeviceIds": ids})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/"+controller.ID+"/rdp-targets", bytes.NewReader(body))
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("replace RDP grants failed: %d %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Targets []repository.RDPTarget `json:"targets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Targets
	}
	if targets := put([]string{first.ID}); len(targets) != 1 || targets[0].DeviceID != first.ID {
		t.Fatalf("target A grant response=%+v", targets)
	}
	if targets := getTargets(); len(targets) != 1 || targets[0].DeviceID != first.ID {
		t.Fatalf("target A was not the only visible host: %+v", targets)
	}
	if ok, err := router.db.AuthorizeRDP(controller.ID, second.ID); err != nil || ok {
		t.Fatalf("unselected target B became connectable: ok=%v err=%v", ok, err)
	}

	if targets := put([]string{second.ID}); len(targets) != 1 || targets[0].DeviceID != second.ID {
		t.Fatalf("target B replacement response=%+v", targets)
	}
	if ok, err := router.db.AuthorizeRDP(controller.ID, first.ID); err != nil || ok {
		t.Fatalf("removed target A remained connectable: ok=%v err=%v", ok, err)
	}
	if ok, err := router.db.AuthorizeRDP(controller.ID, second.ID); err != nil || !ok {
		t.Fatalf("selected target B was not connectable: ok=%v err=%v", ok, err)
	}
}

func TestRDPIngressCreationRejectsDisabledManager(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	router.rdpIngressEnabled = func() bool { return false }

	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "ingress-disabled-fingerprint", InstallationID: "ingress-disabled-install", PublicKey: []byte("ingress-disabled-key"),
		DeviceName: "Ingress Disabled", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"rdp.host", "rdp.public"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"rdp.host", "rdp.public"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/rdp/ingress", bytes.NewReader([]byte(`{"targetDeviceId":"`+device.ID+`","listenPort":33901}`)))
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("rdp.ingress.enabled")) {
		t.Fatalf("disabled ingress was accepted: %d %s", rec.Code, rec.Body.String())
	}
	items, err := router.db.ListRDPIngress("")
	if err != nil || len(items) != 0 {
		t.Fatalf("disabled ingress left an allocation: %+v err=%v", items, err)
	}
}

func TestRDPIngressCreationRollsBackWhenListenerFails(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	router.rdpIngressEnabled = func() bool { return true }
	router.onRDPIngressReload = func(string) error { return errors.New("bind failed") }

	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "ingress-failed-fingerprint", InstallationID: "ingress-failed-install", PublicKey: []byte("ingress-failed-key"),
		DeviceName: "Ingress Failed", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"rdp.host", "rdp.public"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"rdp.host", "rdp.public"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/rdp/ingress", bytes.NewReader([]byte(`{"targetDeviceId":"`+device.ID+`","listenPort":33902}`)))
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !bytes.Contains(rec.Body.Bytes(), []byte("监听失败")) {
		t.Fatalf("listener failure was reported incorrectly: %d %s", rec.Code, rec.Body.String())
	}
	items, err := router.db.ListRDPIngress("")
	if err != nil || len(items) != 0 {
		t.Fatalf("listener failure left an allocation: %+v err=%v", items, err)
	}
}

func TestRDPIngressListReportsLiveUDPReadiness(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	admin := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "ingress-status-fingerprint", InstallationID: "ingress-status-install", PublicKey: []byte("ingress-status-key"),
		DeviceName: "Ingress Status", Platform: "windows", Arch: "amd64",
		RequestedCapabilities: []string{"rdp.host", "rdp.public"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"rdp.host", "rdp.public"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.db.CreateRDPIngress(device.ID, adminUser.ID, 33903, nil, nil, 120, 33900, 34000); err != nil {
		t.Fatal(err)
	}
	router.sessions.Register(&session.DeviceSession{DeviceID: device.ID, Transport: tunnel.TransportTLS})
	router.onRDPIngressStatus = func(string) RDPIngressRuntimeStatus {
		return RDPIngressRuntimeStatus{TCPListening: true, UDPListening: true, ActiveUDP: 1}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/rdp/ingress", nil)
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list ingress failed: %d %s", rec.Code, rec.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil || len(items) != 1 {
		t.Fatalf("invalid ingress response: %s err=%v", rec.Body.String(), err)
	}
	if items[0]["tcpListening"] != true || items[0]["udpListening"] != true || items[0]["activeUdp"] != float64(1) || items[0]["udpEnabled"] != false || items[0]["udpReason"] != "目标隧道不是 QUIC Datagram" {
		t.Fatalf("unexpected UDP readiness response: %+v", items[0])
	}
}
