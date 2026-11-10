package api

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	browsersync "relayproxy/server/browser_sync"
)

func TestBrowserSyncAdminApprovesEnrolledPublicIdentityID(t *testing.T) {
	base, cleanup := setupTestRouter(t)
	defer cleanup()
	identity, err := base.db.CreateIdentity("Browser public identity", "admin")
	if err != nil {
		t.Fatal(err)
	}
	store, err := browsersync.NewStore(base.db.DB)
	if err != nil {
		t.Fatal(err)
	}
	handler := browsersync.NewHandler(store, nil)
	router := NewRouter(base.authService, base.deviceService, base.sessions, base.db, WithBrowserSync(handler))
	publicKey := func() string {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(der)
	}
	device := browsersync.BrowserDevice{
		ID: "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e", Name: "Public ID Chrome",
		RequestedIdentityID: identity.ShortID, SigningKey: publicKey(), EncryptionKey: publicKey(),
	}
	registration, err := json.Marshal(device)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"https://relay.example.com/api/v1/browser-sync/devices/register", bytes.NewReader(registration))
	req.Header.Set("Origin", "chrome-extension://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("public identity enrollment failed: %d %s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	saved, err := store.Device(ctx, device.ID)
	if err != nil || saved.State != "pending" || saved.IdentityID != "" || saved.Send || saved.Receive {
		t.Fatalf("enrollment granted privileges before approval: %+v %v", saved, err)
	}
	body, err := json.Marshal(map[string]any{"identityId": identity.ShortID, "send": true, "receive": true})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/browser-sync/admin/devices/"+device.ID+"/approve", bytes.NewReader(body))
	req.AddCookie(loginAdmin(t, router))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin could not approve the enrolled public ID: %d %s", rec.Code, rec.Body.String())
	}
	saved, err = store.Device(ctx, device.ID)
	if err != nil || saved.State != "approved" || saved.IdentityID != identity.ID ||
		saved.RequestedIdentityID != identity.ShortID || !saved.Send || !saved.Receive {
		t.Fatalf("public ID approval did not retain the correct identity: %+v %v", saved, err)
	}
}
