package browsersync

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func dialApprovedBrowser(t *testing.T, server *httptest.Server, device BrowserDevice, key *ecdsa.PrivateKey) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/api/v1/browser-sync/ws"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: server.Client(),
		HTTPHeader: http.Header{"Origin": []string{"chrome-extension://" + testExtensionID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	sendWSSControl(t, conn, map[string]any{"type": "AUTH_HELLO", "deviceId": device.ID})
	challenge := readWSSControl(t, conn)
	if challenge["type"] != "AUTH_CHALLENGE" {
		t.Fatalf("unexpected challenge: %v", challenge)
	}
	nonce, ok := challenge["challenge"].(string)
	if !ok {
		t.Fatal("missing challenge")
	}
	sendWSSControl(t, conn, map[string]any{
		"type": "AUTH_PROOF", "deviceId": device.ID, "challenge": nonce,
		"signature": signatureFor(t, key, server.URL, device.ID, nonce),
	})
	auth := readWSSControl(t, conn)
	if auth["type"] != "AUTH_OK" || auth["sessionTransferEnabled"] != false {
		t.Fatalf("unexpected auth result: %v", auth)
	}
	return conn
}

func sendWSSControl(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
}
func readWSSControl(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func approvedWSSBrowser(t *testing.T, store *Store, send, receive bool) (BrowserDevice, *ecdsa.PrivateKey) {
	t.Helper()
	key, signSPKI := makeKey(t)
	_, encSPKI := makeKey(t)
	d := BrowserDevice{
		ID: "browser_" + uuid.NewString(), Name: "WSS Chrome", SigningKey: signSPKI, EncryptionKey: encSPKI,
	}
	if _, err := store.Register(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := store.Approve(context.Background(), d.ID, "id_1", send, receive); err != nil {
		t.Fatal(err)
	}
	return d, key
}
func TestWSSBrowserRulePairingAndSessionGate(t *testing.T) {
	store := testBrowserStore(t)
	source, sourceKey := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, sourceKey)
	b := dialApprovedBrowser(t, server, target, targetKey)

	sendWSSControl(t, a, map[string]any{"type": "LIST_PEERS", "requestId": "p1"})
	peers := readWSSControl(t, a)
	if peers["type"] != "PEERS" || peers["requestId"] != "p1" {
		t.Fatalf("peer discovery failed: %v", peers)
	}
	peerList, ok := peers["peers"].([]any)
	if !ok || len(peerList) != 1 {
		t.Fatalf("peer listing incorrect: %v", peers)
	}

	offer := encryptedTestOffer(uuid.NewString(), target.ID)
	sendWSSControl(t, a, map[string]any{"type": "RULE_OFFER", "requestId": "p2", "offer": offer})
	if reply := readWSSControl(t, a); reply["status"] != "offered" {
		t.Fatalf("cannot create encrypted invitation: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{"type": "LIST_RULES", "requestId": "p3"})
	rules := readWSSControl(t, b)
	if rules["type"] != "RULES" {
		t.Fatalf("cannot list target invitations: %v", rules)
	}
	sendWSSControl(t, a, map[string]any{"type": "RULE_CONFIRM", "requestId": "p4", "ruleId": offer.RuleID})
	if reply := readWSSControl(t, a); reply["type"] != "RULE_ERROR" {
		t.Fatalf("rule was activated without B approval: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{"type": "RULE_ACCEPT", "requestId": "p5", "ruleId": offer.RuleID})
	if reply := readWSSControl(t, b); reply["status"] != "target_approved" {
		t.Fatalf("target consent failed: %v", reply)
	}
	sendWSSControl(t, a, map[string]any{"type": "RULE_CONFIRM", "requestId": "p6", "ruleId": offer.RuleID})
	if reply := readWSSControl(t, a); reply["status"] != "active" {
		t.Fatalf("final confirmation failed: %v", reply)
	}
	// Cryptographic cookie payloads are not permitted until end-to-end session
	// verification and per-site conflict checks have independently passed.
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "ruleId": offer.RuleID})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := a.Read(ctx); err == nil {
		t.Fatal("unimplemented session transfer was accepted")
	}
}
