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

	createBody := []byte(`{"name":"Engineering","capabilities":["proxy.client","proxy.exit","rdp.controller","rdp.host"]}`)
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
	if identity.ID == "" || identity.Name != "Engineering" {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	keyReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities/"+identity.ID+"/access-keys",
		bytes.NewReader([]byte(`{"label":"android"}`)))
	keyReq.AddCookie(adminCookie)
	keyRec := httptest.NewRecorder()
	router.ServeHTTP(keyRec, keyReq)
	if keyRec.Code != http.StatusCreated {
		t.Fatalf("issue access key failed: %d %s", keyRec.Code, keyRec.Body.String())
	}
	var issued repository.IssuedIdentityAccessKey
	if err := json.Unmarshal(keyRec.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.ID == "" || issued.AccessKey == "" {
		t.Fatalf("access key was not returned once: %+v", issued)
	}

	listKeysReq := httptest.NewRequest(http.MethodGet, "/api/v1/identities/"+identity.ID+"/access-keys", nil)
	listKeysReq.AddCookie(adminCookie)
	listKeysRec := httptest.NewRecorder()
	router.ServeHTTP(listKeysRec, listKeysReq)
	if listKeysRec.Code != http.StatusOK {
		t.Fatalf("list access keys failed: %d %s", listKeysRec.Code, listKeysRec.Body.String())
	}
	if bytes.Contains(listKeysRec.Body.Bytes(), []byte("accessKey")) || bytes.Contains(listKeysRec.Body.Bytes(), []byte(issued.AccessKey)) {
		t.Fatalf("access key plaintext leaked from list response: %s", listKeysRec.Body.String())
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

	revokeReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/identities/"+identity.ID+"/access-keys/"+issued.ID, nil)
	revokeReq.AddCookie(adminCookie)
	revokeRec := httptest.NewRecorder()
	router.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("revoke access key failed: %d %s", revokeRec.Code, revokeRec.Body.String())
	}
	if _, err := router.db.ResolveIdentityAccessKey(issued.AccessKey); err == nil {
		t.Fatal("revoked API access key still resolved")
	}
}

func TestIdentityAPIRequiresRevisionForSafeConcurrentUpdate(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/identities",
		bytes.NewReader([]byte(`{"name":"Revision Test","capabilities":["proxy.client"]}`)))
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
