package browsersync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const unifiedExtensionID = "cccccccccccccccccccccccccccccccc"
const unifiedDeviceID = "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e"

func unifiedRegistration(t *testing.T) BrowserDevice {
	t.Helper()
	_, signing := makeKey(t)
	_, encryption := makeKey(t)
	return BrowserDevice{
		ID: unifiedDeviceID, Name: "Chrome approval test",
		SigningKey: signing, EncryptionKey: encryption,
	}
}

func postEnrollment(t *testing.T, h *Handler, extensionID string, device any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(device)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"https://relay.example.com/api/v1/browser-sync/extensions/register", bytes.NewReader(body))
	req.Header.Set("Origin", "chrome-extension://"+extensionID)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.12:12345"
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	return out
}

func TestUnifiedEnrollmentRequiresOnlyOneAdminApproval(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, nil)
	device := unifiedRegistration(t)
	ctx := context.Background()
	out := postEnrollment(t, h, unifiedExtensionID, device)
	if out.Code != http.StatusAccepted {
		t.Fatalf("one-shot registration returned %d: %s", out.Code, out.Body)
	}
	var response struct {
		ID             string `json:"id"`
		State          string `json:"state"`
		ExtensionID    string `json:"extensionId"`
		ExtensionState string `json:"extensionState"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != device.ID || response.ExtensionID != unifiedExtensionID ||
		response.State != "pending" || response.ExtensionState != "pending" {
		t.Fatalf("unexpected initial enrollment: %+v", response)
	}
	saved, err := store.Device(ctx, device.ID)
	if err != nil || saved.Send || saved.Receive || saved.IdentityID != "" ||
		saved.ExtensionID != unifiedExtensionID || saved.ExtensionState != "pending" {
		t.Fatalf("unauthorized enrollment leaked capabilities: %+v %v", saved, err)
	}
	if store.ExtensionApproved(ctx, unifiedExtensionID) {
		t.Fatal("registration unexpectedly approved the extension")
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Device(ctx, device.ID)
	if err != nil || saved.State != "approved" || !saved.Send || saved.Receive ||
		saved.IdentityID != "id_1" || saved.ExtensionState != "approved" ||
		!store.ExtensionApproved(ctx, unifiedExtensionID) {
		t.Fatalf("one admin action did not approve both: %+v %v", saved, err)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", false, true); err == nil {
		t.Fatal("already-approved browser was reapproved")
	}
}

func TestUnifiedEnrollmentCannotSetPrivilegesOrSwapExtension(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, nil)
	device := unifiedRegistration(t)
	ctx := context.Background()
	privileged := map[string]any{
		"id": device.ID, "name": device.Name, "signingPublicKey": device.SigningKey,
		"encryptionPublicKey": device.EncryptionKey, "identityId": "id_1", "send": true,
	}
	if out := postEnrollment(t, h, unifiedExtensionID, privileged); out.Code != http.StatusForbidden {
		t.Fatalf("client-selected privileges accepted: %d", out.Code)
	}
	if out := postEnrollment(t, h, unifiedExtensionID, device); out.Code != http.StatusAccepted {
		t.Fatalf("valid registration rejected: %d", out.Code)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "nonexistent", true, false); err == nil {
		t.Fatal("unknown identity was accepted")
	}
	got, err := store.Device(ctx, device.ID)
	if err != nil || got.State != "pending" || got.ExtensionState != "pending" {
		t.Fatalf("partial approval was persisted: %+v %v", got, err)
	}
	const swappedExtension = "dddddddddddddddddddddddddddddddd"
	if out := postEnrollment(t, h, swappedExtension, device); out.Code != http.StatusConflict {
		t.Fatalf("new extension rebound existing browser identity: %d", out.Code)
	}
	_, newSigning := makeKey(t)
	forged := device
	forged.SigningKey = newSigning
	if out := postEnrollment(t, h, unifiedExtensionID, forged); out.Code != http.StatusConflict {
		t.Fatalf("changed signing public key replaced browser key: %d", out.Code)
	}
}

func TestUnifiedEnrollmentRevokedExtensionCannotEnrollNewDevices(t *testing.T) {
	store := testBrowserStore(t)
	h := NewHandler(store, nil)
	ctx := context.Background()
	device := unifiedRegistration(t)
	if out := postEnrollment(t, h, unifiedExtensionID, device); out.Code != http.StatusAccepted {
		t.Fatal(out.Code)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, true); err != nil {
		t.Fatal(err)
	}
	second := unifiedRegistration(t)
	second.ID = "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d60"
	if out := postEnrollment(t, h, unifiedExtensionID, second); out.Code != http.StatusAccepted {
		t.Fatalf("approved extension could not request new device: %d", out.Code)
	}
	pending, err := store.Device(ctx, second.ID)
	if err != nil || pending.State != "pending" || pending.Send || pending.Receive {
		t.Fatalf("new browser bypassed individual approval: %+v %v", pending, err)
	}
	if err := store.SetExtensionApproved(ctx, unifiedExtensionID, false); err != nil {
		t.Fatal(err)
	}
	third := unifiedRegistration(t)
	third.ID = "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d61"
	if out := postEnrollment(t, h, unifiedExtensionID, third); out.Code != http.StatusForbidden {
		t.Fatalf("revoked extension enrolled a new browser: %d", out.Code)
	}
	if err := store.ApproveEnrollment(ctx, second.ID, "id_1", true, false); err == nil {
		t.Fatal("revoked extension was reapproved through pending browser")
	}
}
