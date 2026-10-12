// Command wss-fixture starts a throwaway loopback-only RelayProxy Browser Sync
// Server for real Chromium CI. It is NOT built into any shipping binary.
// Production authentication, registration and relay paths are used unchanged;
// only this test binary exposes an ephemeral, bearer-guarded approval endpoint.
package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
	browsersync "relayproxy/server/browser_sync"
)

func main() {
	log.SetOutput(os.Stderr)
	token := os.Getenv("RELAY_BROWSER_WSS_FIXTURE_TOKEN")
	if len(token) < 32 {
		log.Fatal("missing ephemeral fixture token")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "CREATE TABLE identities(id TEXT PRIMARY KEY,status TEXT NOT NULL)"); err != nil {
		log.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO identities(id,status) VALUES('id_1','active')"); err != nil {
		log.Fatal(err)
	}
	store, err := browsersync.NewStore(db)
	if err != nil {
		log.Fatal(err)
	}
	relay := httptest.NewTLSServer(browsersync.NewHandler(store, nil))
	defer relay.Close()

	// Only the local test controller may approve/revoke devices. Do not
	// accept identities or capabilities from the browser registration HTTP
	// request, and do not expose this listener on non-loopback interfaces.
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/__fixture/device", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if req.Method != http.MethodPost ||
			subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "not authorized", http.StatusForbidden)
			return
		}
		var body struct {
			Action  string `json:"action"`
			ID      string `json:"id"`
			Send    bool   `json:"send"`
			Receive bool   `json:"receive"`
		}
		req.Body = http.MaxBytesReader(w, req.Body, 2048)
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var actionErr error
		switch body.Action {
		case "approve":
			actionErr = store.Approve(req.Context(), body.ID, "id_1", body.Send, body.Receive)
		case "revoke":
			actionErr = store.Revoke(req.Context(), body.ID)
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if actionErr != nil {
			log.Printf("fixture device action rejected: %v", actionErr)
			http.Error(w, "device transition rejected", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// A unique temporary administration token is never emitted on stdout or
	// any HTTP response. The ephemeral server URL itself contains no token.
	admin := httptest.NewServer(adminMux)
	defer admin.Close()

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		log.Fatal(err)
	}
	// Assert both URLs are loopback before surfacing them to the test runner.
	for _, value := range []string{relay.URL, admin.URL} {
		if value == "" {
			log.Fatal("fixture listener unavailable")
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{
		"serverOrigin": relay.URL,
		"adminOrigin":  admin.URL,
		"runId":        hex.EncodeToString(nonce),
	}); err != nil {
		log.Fatal(fmt.Errorf("fixture startup: %w", err))
	}

	interrupt, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-interrupt.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = admin.Config.Shutdown(shutdown)
	_ = relay.Config.Shutdown(shutdown)
}
