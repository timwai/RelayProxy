package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"relayproxy/server/repository"
)

func TestSystemIdentityGrantAdminAPILifecycle(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	identity, err := router.db.CreateIdentity("Server Exit API", "admin")
	if err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	router.onIdentityAuthorizationChanged = func(identityID, accessKeyID string) {
		if identityID != identity.ID || accessKeyID != "" {
			t.Fatalf("unexpected authorization refresh: identity=%s key=%s", identityID, accessKeyID)
		}
		refreshes++
	}

	createBody, _ := json.Marshal(map[string]any{
		"resourceId":        repository.SystemResourceServerExit,
		"granteeIdentityId": identity.ID,
		"features":          []string{repository.GrantFeatureProxyUse},
	})
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/system-identity-grants", bytes.NewReader(createBody))
	createReq.AddCookie(adminCookie)
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create system grant failed: %d %s", createRec.Code, createRec.Body.String())
	}
	var grant repository.SystemIdentityGrant
	if err := json.Unmarshal(createRec.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	if grant.ID == "" || grant.ResourceID != repository.SystemResourceServerExit || grant.Revision != 1 {
		t.Fatalf("unexpected system grant: %+v", grant)
	}
	if refreshes != 1 {
		t.Fatalf("create refresh count=%d want=1", refreshes)
	}

	listReq := httptest.NewRequest(http.MethodGet,
		"/api/v1/system-identity-grants?resourceId="+repository.SystemResourceServerExit, nil)
	listReq.AddCookie(adminCookie)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list system grants failed: %d %s", listRec.Code, listRec.Body.String())
	}
	var listed []repository.SystemIdentityGrant
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != grant.ID {
		t.Fatalf("unexpected system grant list: %+v", listed)
	}

	updateReq := httptest.NewRequest(http.MethodPatch, "/api/v1/system-identity-grants/"+grant.ID,
		bytes.NewReader([]byte(`{"features":["proxy.use"],"clearExpiresAt":true,"revision":1}`)))
	updateReq.AddCookie(adminCookie)
	updateRec := httptest.NewRecorder()
	router.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update system grant failed: %d %s", updateRec.Code, updateRec.Body.String())
	}
	var updated repository.SystemIdentityGrant
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || refreshes != 2 {
		t.Fatalf("unexpected updated system grant=%+v refreshes=%d", updated, refreshes)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete,
		"/api/v1/system-identity-grants/"+grant.ID+"?revision=2", nil)
	deleteReq.AddCookie(adminCookie)
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete system grant failed: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
	if refreshes != 3 {
		t.Fatalf("delete refresh count=%d want=3", refreshes)
	}
}

func TestSystemIdentityGrantAPIRejectsUnsupportedFeature(t *testing.T) {
	router, cleanup := setupTestRouter(t)
	defer cleanup()
	adminCookie := loginAdmin(t, router)

	identity, err := router.db.CreateIdentity("Server Exit Reject", "admin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"resourceId":        repository.SystemResourceServerExit,
		"granteeIdentityId": identity.ID,
		"features":          []string{repository.GrantFeatureRDPConnect},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system-identity-grants", bytes.NewReader(body))
	req.AddCookie(adminCookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported system feature returned %d: %s", rec.Code, rec.Body.String())
	}
}
