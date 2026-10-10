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
	if auth["type"] != "AUTH_OK" || auth["sessionTransferEnabled"] != true {
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
	// Malformed or unsigned session payloads are rejected while authenticated
	// control-plane traffic is still accepted.
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "bad1", "ruleId": offer.RuleID})
	if reply := readWSSControl(t, a); reply["type"] != "RULE_ERROR" {
		t.Fatalf("unsigned session was not rejected: %v", reply)
	}
}

func TestWSSSignedEncryptedSessionDeliveredWithAck(t *testing.T) {
	store := testBrowserStore(t)
	source, sourceKey := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	if err := store.OfferRule(context.Background(), source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(context.Background(), target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(context.Background(), source.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, sourceKey)
	b := dialApprovedBrowser(t, server, target, targetKey)

	// The receiver cannot manufacture a successful login confirmation for a
	// message the source never sent, even though its own device is approved.
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_ACK", "requestId": "forged", "ruleId": ruleID,
		"messageId": uuid.NewString(), "status": "APPLIED",
	})
	if reply := readWSSControl(t, b); reply["type"] != "RULE_ERROR" {
		t.Fatalf("accepted ACK for unknown message: %v", reply)
	}

	env := signedTestSnapshot(t, sourceKey, source.ID, target.ID, ruleID)
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "send1", "envelope": env})
	if reply := readWSSControl(t, a); reply["type"] != "SYNC_STATUS" || reply["status"] != "RELAYED" {
		t.Fatalf("source missing delivery status: %v", reply)
	}
	delivered := readWSSControl(t, b)
	if delivered["type"] != "SESSION_SNAPSHOT" {
		t.Fatalf("target did not receive an opaque encrypted payload: %v", delivered["type"])
	}
	push, ok := delivered["envelope"].(map[string]any)
	if !ok || push["ciphertext"] != env.Ciphertext {
		t.Fatal("ciphertext altered by server")
	}
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_ACK", "requestId": "ack1", "ruleId": ruleID,
		"messageId": env.MessageID, "status": "APPLIED",
	})
	pushed := readWSSControl(t, a)
	if pushed["type"] != "SYNC_ACK" || pushed["status"] != "APPLIED" {
		t.Fatalf("source did not get restricted ACK: %v", pushed)
	}
	if reply := readWSSControl(t, b); reply["type"] != "SYNC_STATUS" {
		t.Fatalf("target missing ACK forwarding status: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_ACK", "requestId": "replayedAck", "ruleId": ruleID,
		"messageId": env.MessageID, "status": "APPLIED",
	})
	if reply := readWSSControl(t, b); reply["type"] != "RULE_ERROR" {
		t.Fatalf("accepted duplicate terminal ACK: %v", reply)
	}
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "dupe", "envelope": env})
	if reply := readWSSControl(t, a); reply["type"] != "RULE_ERROR" {
		t.Fatalf("server accepted replayed ciphertext: %v", reply)
	}
}

func TestWSSRecoversSequenceAndTerminalDeliveryAfterReconnect(t *testing.T) {
	store := testBrowserStore(t)
	source, sourceKey := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	if err := store.OfferRule(context.Background(), source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil { t.Fatal(err) }
	if err := store.AcceptRule(context.Background(), target.ID, ruleID); err != nil { t.Fatal(err) }
	if err := store.ConfirmRule(context.Background(), source.ID, ruleID); err != nil { t.Fatal(err) }
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, sourceKey)
	b := dialApprovedBrowser(t, server, target, targetKey)

	sendWSSControl(t, a, map[string]any{"type":"SEQUENCE_CURSOR","requestId":"cursor0","ruleId":ruleID})
	reply := readWSSControl(t, a)
	if reply["type"]!="SESSION_CURSOR" || reply["lastSequence"]!=float64(0) {
		t.Fatalf("fresh sequence cursor: %v", reply)
	}
	env := signedTestSnapshot(t, sourceKey, source.ID, target.ID, ruleID)
	sendWSSControl(t, a, map[string]any{"type":"SESSION_SNAPSHOT","requestId":"sent","envelope":env})
	if reply := readWSSControl(t,a);reply["status"]!="RELAYED" {t.Fatalf("relay failed: %v",reply)}
	if msg:=readWSSControl(t,b);msg["type"]!="SESSION_SNAPSHOT" {t.Fatalf("target did not receive envelope: %v",msg)}
	sendWSSControl(t,a,map[string]any{"type":"DELIVERY_STATUS","requestId":"query1","ruleId":ruleID,"messageId":env.MessageID})
	if reply:=readWSSControl(t,a);reply["status"]!="PENDING" {t.Fatalf("expected pending delivery: %v",reply)}
	// Simulate MV3 worker stop: B's terminal ACK must persist even if the
	// source no longer has a live WebSocket.
	_ = a.CloseNow()
	sendWSSControl(t,b,map[string]any{"type":"SYNC_ACK","requestId":"ack","ruleId":ruleID,"messageId":env.MessageID,"status":"APPLIED"})
	if reply:=readWSSControl(t,b);reply["type"]!="SYNC_STATUS" {t.Fatalf("offline terminal ACK not accepted: %v",reply)}
	a2:=dialApprovedBrowser(t,server,source,sourceKey)
	sendWSSControl(t,a2,map[string]any{"type":"DELIVERY_STATUS","requestId":"query2","ruleId":ruleID,"messageId":env.MessageID})
	if reply:=readWSSControl(t,a2);reply["type"]!="DELIVERY_RESULT" || reply["status"]!="APPLIED" {
		t.Fatalf("terminal ACK lost on source reconnect: %v",reply)
	}
	sendWSSControl(t,a2,map[string]any{"type":"SEQUENCE_CURSOR","requestId":"cursor1","ruleId":ruleID})
	if reply:=readWSSControl(t,a2);reply["lastSequence"]!=float64(1) {
		t.Fatalf("accepted sequence cursor lost on reconnect: %v",reply)
	}
	sendWSSControl(t,b,map[string]any{"type":"DELIVERY_STATUS","requestId":"unauthorized","ruleId":ruleID,"messageId":env.MessageID})
	if reply:=readWSSControl(t,b);reply["type"]!="RULE_ERROR" {
		t.Fatalf("receiver read source-only delivery result: %v",reply)
	}
}
