package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"

	browsersync "relayproxy/server/browser_sync"
)

func TestBrowserSyncAdminDeleteRequiresRevocation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := browsersync.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	router := &Router{browserSync: browsersync.NewHandler(store, nil)}
	id := "browser_69064be2-ea88-4e8d-9aa1-a0c55e7a0d5e"
	_, err = store.Register(context.Background(), browsersync.BrowserDevice{
		ID: id, Name: "Old Chrome", SigningKey: "fake-public-test-key", EncryptionKey: "fake-encryption-test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(id string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sync/admin/devices/"+id, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		router.handleBrowserSyncDelete(rec, req)
		return rec.Code
	}
	if got := call(id); got != http.StatusConflict {
		t.Fatalf("active enrollment deleted: %d", got)
	}
	if err := store.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := call(id); got != http.StatusOK {
		t.Fatalf("revoked enrollment could not be deleted: %d", got)
	}
	if got := call(id); got != http.StatusNotFound {
		t.Fatalf("missing device returned %d", got)
	}
}

func TestBrowserSyncAdminExtensionDeleteRequiresRevocation(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := browsersync.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	router := &Router{browserSync: browsersync.NewHandler(store, nil)}
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := store.RequestExtension(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	call := func() int {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sync/admin/extensions/"+id, nil)
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		router.handleBrowserSyncExtensionDelete(rec, req)
		return rec.Code
	}
	if got := call(); got != http.StatusConflict {
		t.Fatalf("pending extension deleted: %d", got)
	}
	if err := store.SetExtensionApproved(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetExtensionApproved(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	if got := call(); got != http.StatusOK {
		t.Fatalf("revoked extension not deleted: %d", got)
	}
	if got := call(); got != http.StatusNotFound {
		t.Fatalf("missing extension returned %d", got)
	}
}
