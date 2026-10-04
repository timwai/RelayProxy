package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"relayproxy/server/repository"
)

func TestIdentityAdminAPIsAndDeviceAssignment(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)
	adminUser, err := router.db.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}

	invalidReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities",
		bytes.NewReader([]byte(`{"name":"Invalid","capabilities":["proxy.client"]}`)))
	invalidReq.AddCookie(adminCookie)
	invalidRec := httptest.NewRecorder()
	router.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("identity capability field was accepted: %d %s", invalidRec.Code, invalidRec.Body.String())
	}
	clientIDReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities",
		bytes.NewReader([]byte(`{"shortId":"a1b2c3d4e5f6g7h8","username":"chosen.owner","name":"Chosen","password":"identity-pass-123"}`)))
	clientIDReq.AddCookie(adminCookie)
	clientIDRec := httptest.NewRecorder()
	router.ServeHTTP(clientIDRec, clientIDReq)
	if clientIDRec.Code != http.StatusBadRequest {
		t.Fatalf("client-selected identity id was accepted: %d %s", clientIDRec.Code, clientIDRec.Body.String())
	}

	createBody := []byte(`{"username":"engineering.owner","name":"Engineering","password":"identity-pass-123"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities", bytes.NewReader(createBody))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create identity failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var identity repository.Identity
	if err := json.Unmarshal(createRec.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if identity.ID == "" || len(identity.ShortID) != repository.IdentityPublicIDLength || identity.LoginUsername != "engineering.owner" || identity.Name != "Engineering" || !identity.LoginConfigured || bytes.Contains(createRec.Body.Bytes(), []byte("capabilities")) {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader([]byte(`{"username":"engineering.owner","password":"identity-pass-123"}`)))
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("identity login failed: %d %s", loginRec.Code, loginRec.Body.String())
	}

	resetReq := httptest.NewRequest(http.MethodPut, "/api/v1/identities/"+identity.ID+"/password",
		bytes.NewReader([]byte(`{"username":"engineering.admin","password":"identity-pass-456"}`)))
	resetReq.AddCookie(adminCookie)
	resetRec := httptest.NewRecorder()
	router.ServeHTTP(resetRec, resetReq)
	if resetRec.Code != http.StatusOK {
		t.Fatalf("rename identity login failed: %d %s", resetRec.Code, resetRec.Body.String())
	}
	if _, err := router.db.GetUserByUsername("engineering.owner"); err == nil {
		t.Fatal("old identity login username still exists")
	}
	if user, err := router.db.GetUserByUsername("engineering.admin"); err != nil || user.IdentityID != identity.ID {
		t.Fatalf("custom identity login username was not saved: %+v err=%v", user, err)
	}

	pending, err := router.db.ObserveDeviceIdentity(repository.DeviceIdentityObservation{
		Fingerprint: "identity-api-device", InstallationID: "identity-api-install",
		PublicKey: []byte("identity-api-public-key"), DeviceName: "Identity API Device",
		Platform: "windows", Arch: "amd64", RequestedCapabilities: []string{"proxy.client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	device, err := router.db.ApproveEnrollment(pending.RequestID, adminUser.ID, []string{"proxy.client"})
	if err != nil {
		t.Fatal(err)
	}

	assignBody, _ := json.Marshal(map[string]string{"identityId": identity.ID})
	assignReq := httptest.NewRequest(http.MethodPut, "/api/v1/devices/"+device.ID+"/identity", bytes.NewReader(assignBody))
	assignReq.AddCookie(adminCookie)
	assignRec := httptest.NewRecorder()
	router.ServeHTTP(assignRec, assignReq)
	if assignRec.Code != http.StatusOK {
		t.Fatalf("assign identity failed: %d %s", assignRec.Code, assignRec.Body.String())
	}
	var assignment repository.DeviceIdentitySummary
	if err := json.Unmarshal(assignRec.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.IdentityID != identity.ID || assignment.IdentityName != identity.Name {
		t.Fatalf("unexpected assignment: %+v", assignment)
	}

	clearReq := httptest.NewRequest(http.MethodPut, "/api/v1/devices/"+device.ID+"/identity",
		bytes.NewReader([]byte(`{"identityId":""}`)))
	clearReq.AddCookie(adminCookie)
	clearRec := httptest.NewRecorder()
	router.ServeHTTP(clearRec, clearReq)
	if clearRec.Code != http.StatusBadRequest {
		t.Fatalf("clearing required identity returned %d: %s", clearRec.Code, clearRec.Body.String())
	}

	devicesReq := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
	devicesReq.AddCookie(adminCookie)
	devicesRec := httptest.NewRecorder()
	router.ServeHTTP(devicesRec, devicesReq)
	if devicesRec.Code != http.StatusOK {
		t.Fatalf("list devices failed: %d %s", devicesRec.Code, devicesRec.Body.String())
	}
	var devices []map[string]any
	if err := json.Unmarshal(devicesRec.Body.Bytes(), &devices); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range devices {
		if item["id"] == device.ID {
			found = item["identityId"] == identity.ID && item["identityName"] == identity.Name
		}
	}
	if !found {
		t.Fatalf("device identity missing from list: %s", devicesRec.Body.String())
	}

}

func TestIdentityAPIRequiresRevisionForSafeConcurrentUpdate(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities",
		bytes.NewReader([]byte(`{"username":"revision.owner","name":"Revision Test","password":"identity-pass-123"}`)))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create identity failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var identity repository.Identity
	if err := json.Unmarshal(createRec.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}

	firstReq := httptest.NewRequest(http.MethodPatch, "/api/v1/identities/"+identity.ID,
		bytes.NewReader([]byte(`{"name":"Revision Test 2","policyRevision":1}`)))
	firstReq.AddCookie(adminCookie)
	firstRec := httptest.NewRecorder()
	router.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first identity update failed: %d %s", firstRec.Code, firstRec.Body.String())
	}

	staleReq := httptest.NewRequest(http.MethodPatch, "/api/v1/identities/"+identity.ID,
		bytes.NewReader([]byte(`{"status":"disabled","policyRevision":1}`)))
	staleReq.AddCookie(adminCookie)
	staleRec := httptest.NewRecorder()
	router.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale identity update returned %d: %s", staleRec.Code, staleRec.Body.String())
	}
}
