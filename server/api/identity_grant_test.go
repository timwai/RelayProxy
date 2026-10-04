package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"relayproxy/server/repository"
	"relayproxy/server/service"
)

func seedGrantAPIDevice(t *testing.T, db *repository.DB, id, identityID string, capabilities []string) {
	t.Helper()
	device := &repository.Device{
		ID: id, Name: id, Fingerprint: "fp-" + id, InstallationID: "install-" + id,
		ApprovalState:         repository.EnrollmentApproved,
		RequestedCapabilities: append([]string(nil), capabilities...),
		ApprovedCapabilities:  append([]string(nil), capabilities...),
	}
	if err := db.UpsertDevice(device); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetDeviceIdentity(id, identityID, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceIdentityGrantAdminAPILifecycle(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	targetIdentity, err := router.db.CreateIdentity("Target Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	granteeIdentity, err := router.db.CreateIdentity("Grantee Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	seedGrantAPIDevice(t, router.db, "grant-api-target", targetIdentity.ID, []string{"proxy.exit", "rdp.host"})

	var callbacks [][2]string
	router.onDeviceIdentityGrantChanged = func(targetDeviceID, granteeIdentityID string) {
		callbacks = append(callbacks, [2]string{targetDeviceID, granteeIdentityID})
	}

	createBody, _ := json.Marshal(map[string]any{
		"targetDeviceId":    "grant-api-target",
		"granteeIdentityId": granteeIdentity.ID,
		"features":          []string{repository.GrantFeatureProxyUse},
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/device-identity-grants", bytes.NewReader(createBody))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create grant failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var grant repository.DeviceIdentityGrant
	if err := json.Unmarshal(createRec.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.ID == "" || grant.TargetDeviceID != "grant-api-target" || grant.GranteeIdentityID != granteeIdentity.ID || grant.Revision != 1 {
		t.Fatalf("unexpected grant: %+v", grant)
	}
	if len(callbacks) != 1 || callbacks[0][0] != grant.TargetDeviceID || callbacks[0][1] != granteeIdentity.ID {
		t.Fatalf("create callback mismatch: %+v", callbacks)
	}

	listReq := httptest.NewRequest(http.MethodGet,
		"/api/v1/device-identity-grants?granteeIdentityId="+granteeIdentity.ID+"&feature="+repository.GrantFeatureProxyUse, nil)
	listReq.AddCookie(adminCookie)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list grants failed: %d %s", listRec.Code, listRec.Body.String())
	}
	var listed []repository.DeviceIdentityGrant
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != grant.ID {
		t.Fatalf("unexpected grant list: %+v", listed)
	}

	updateBody, _ := json.Marshal(map[string]any{
		"features": []string{repository.GrantFeatureProxyUse, repository.GrantFeatureRDPConnect},
		"revision": grant.Revision,
	})
	updateReq := httptest.NewRequest(http.MethodPatch, "/api/v1/device-identity-grants/"+grant.ID, bytes.NewReader(updateBody))
	updateReq.AddCookie(adminCookie)
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update grant failed: %d %s", updateRec.Code, updateRec.Body.String())
	}
	var updated repository.DeviceIdentityGrant
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || len(updated.Features) != 2 {
		t.Fatalf("unexpected updated grant: %+v", updated)
	}
	if len(callbacks) != 2 {
		t.Fatalf("update callback count=%d want=2", len(callbacks))
	}

	staleReq := httptest.NewRequest(http.MethodPatch, "/api/v1/device-identity-grants/"+grant.ID,
		bytes.NewReader([]byte(`{"features":["proxy.use"],"revision":1}`)))
	staleReq.AddCookie(adminCookie)
	staleRec := httptest.NewRecorder()
	router.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale grant update returned %d: %s", staleRec.Code, staleRec.Body.String())
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/v1/device-identity-grants/"+grant.ID,
			bytes.NewReader([]byte(`{"features":["proxy.use"]}`)))
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s without revision: %d %s", method, rec.Code, rec.Body.String())
		}
	}

	deleteReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/device-identity-grants/"+grant.ID+"?revision=2", nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete grant failed: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := router.db.GetDeviceIdentityGrant(grant.ID); err == nil {
		t.Fatal("deleted grant still exists")
	}
	if len(callbacks) != 3 {
		t.Fatalf("delete callback count=%d want=3", len(callbacks))
	}
	repeatedReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/device-identity-grants/"+grant.ID+"?revision=2", nil)
	repeatedReq.AddCookie(adminCookie)
	repeatedRec := httptest.NewRecorder()
	router.ServeHTTP(repeatedRec, repeatedReq)
	if repeatedRec.Code != http.StatusOK || len(callbacks) != 3 {
		t.Fatalf("repeated delete: %d %s callbacks=%d", repeatedRec.Code, repeatedRec.Body.String(), len(callbacks))
	}
}

func TestIdentityLoginManagesOnlyItsOwnDeviceGrants(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	hash, err := service.HashPassword("identity-password")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := router.db.CreateIdentityWithLogin("grant-owner", "Grant Owner", "admin", hash)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner, err := router.db.CreateIdentityWithLogin("other-owner", "Other Owner", "admin", hash)
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := router.db.CreateIdentityWithLogin("grant-user", "Grant User", "admin", hash)
	if err != nil {
		t.Fatal(err)
	}
	seedGrantAPIDevice(t, router.db, "identity-owned-target", owner.ID, []string{"proxy.exit", "rdp.host"})
	seedGrantAPIDevice(t, router.db, "other-owned-target", otherOwner.ID, []string{"proxy.exit"})
	token, _, err := router.authService.Login(owner.LoginUsername, "identity-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "relay_session", Value: token}

	create := func(target string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"targetDeviceId": target, "granteeIdentityId": grantee.ID,
			"features": []string{repository.GrantFeatureProxyUse, repository.GrantFeatureRDPConnect},
		})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/device-identity-grants", bytes.NewReader(body))
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := create("other-owned-target"); rec.Code != http.StatusNotFound {
		t.Fatalf("identity managed another identity's device: %d %s", rec.Code, rec.Body.String())
	}
	rec := create("identity-owned-target")
	if rec.Code != http.StatusCreated {
		t.Fatalf("identity could not grant its own device: %d %s", rec.Code, rec.Body.String())
	}
	var grant repository.DeviceIdentityGrant
	if err := json.Unmarshal(rec.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/device-identity-grants?targetDeviceId=identity-owned-target", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("identity could not list its device grants: %d %s", listRec.Code, listRec.Body.String())
	}
	var listed []repository.DeviceIdentityGrant
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].ID != grant.ID {
		t.Fatalf("unexpected identity grant list: %+v err=%v", listed, err)
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/device-identity-grants/"+grant.ID+"?revision=1", nil)
	deleteReq.AddCookie(cookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("identity could not delete its device grant: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestDeviceIdentityGrantAPIRejectsSameIdentity(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	identity, err := router.db.CreateIdentity("Same Identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	seedGrantAPIDevice(t, router.db, "same-grant-target", identity.ID, []string{"proxy.exit"})

	body, _ := json.Marshal(map[string]any{
		"targetDeviceId":    "same-grant-target",
		"granteeIdentityId": identity.ID,
		"features":          []string{repository.GrantFeatureProxyUse},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/device-identity-grants", bytes.NewReader(body))
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same-identity grant returned %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeviceIdentityMoveRefreshesAffectedIdentities(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	source, err := router.db.CreateIdentity("Move Source", "admin")
	if err != nil {
		t.Fatal(err)
	}
	destination, err := router.db.CreateIdentity("Move Destination", "admin")
	if err != nil {
		t.Fatal(err)
	}
	grantee, err := router.db.CreateIdentity("Move Grantee", "admin")
	if err != nil {
		t.Fatal(err)
	}
	seedGrantAPIDevice(t, router.db, "move-api-target", source.ID, []string{"proxy.exit"})
	grant, err := router.db.CreateDeviceIdentityGrant(
		"move-api-target", grantee.ID, "admin", []string{repository.GrantFeatureProxyUse}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	refreshed := map[string]int{}
	router.onIdentityAuthorizationChanged = func(identityID string) {
		refreshed[identityID]++
	}

	body, _ := json.Marshal(map[string]string{"identityId": destination.ID})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/move-api-target/identity", bytes.NewReader(body))
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("move device identity failed: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := router.db.GetDeviceIdentityGrant(grant.ID); err == nil {
		t.Fatal("identity move left the target share in the database")
	}
	for _, identityID := range []string{source.ID, destination.ID, grantee.ID} {
		if refreshed[identityID] != 1 {
			t.Fatalf("identity %s refresh count=%d want=1; all=%v", identityID, refreshed[identityID], refreshed)
		}
	}
	if len(refreshed) != 3 {
		t.Fatalf("unexpected identity refresh set: %v", refreshed)
	}
}
