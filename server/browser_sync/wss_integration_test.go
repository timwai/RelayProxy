package browsersync

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	protocol "relayproxy/internal/browser_sync"
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
	// The optional HTTPS CONNECT proxy is disabled by default, and normal
	// Cookie-only rules must not fail their network binding status query.
	sendWSSControl(t, b, map[string]any{"type": "NETWORK_BINDING", "requestId": "no-network", "ruleId": offer.RuleID})
	bindingReply := readWSSControl(t, b)
	binding, ok := bindingReply["binding"].(map[string]any)
	if bindingReply["type"] != "NETWORK_BINDING" || !ok || binding["status"] != "none" {
		t.Fatalf("disabled network mode unexpectedly changed Cookie-only rules: %v", bindingReply)
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

	sendWSSControl(t, a, map[string]any{"type": "SEQUENCE_CURSOR", "requestId": "cursor0", "ruleId": ruleID})
	reply := readWSSControl(t, a)
	if reply["type"] != "SESSION_CURSOR" || reply["lastSequence"] != float64(0) {
		t.Fatalf("fresh sequence cursor: %v", reply)
	}
	env := signedTestSnapshot(t, sourceKey, source.ID, target.ID, ruleID)
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "sent", "envelope": env})
	if reply := readWSSControl(t, a); reply["status"] != "RELAYED" {
		t.Fatalf("relay failed: %v", reply)
	}
	if msg := readWSSControl(t, b); msg["type"] != "SESSION_SNAPSHOT" {
		t.Fatalf("target did not receive envelope: %v", msg)
	}
	sendWSSControl(t, a, map[string]any{"type": "DELIVERY_STATUS", "requestId": "query1", "ruleId": ruleID, "messageId": env.MessageID})
	if reply := readWSSControl(t, a); reply["status"] != "PENDING" {
		t.Fatalf("expected pending delivery: %v", reply)
	}
	// Simulate MV3 worker stop: B's terminal ACK must persist even if the
	// source no longer has a live WebSocket.
	_ = a.CloseNow()
	sendWSSControl(t, b, map[string]any{"type": "SYNC_ACK", "requestId": "ack", "ruleId": ruleID, "messageId": env.MessageID, "status": "APPLIED"})
	if reply := readWSSControl(t, b); reply["type"] != "SYNC_STATUS" {
		t.Fatalf("offline terminal ACK not accepted: %v", reply)
	}
	a2 := dialApprovedBrowser(t, server, source, sourceKey)
	sendWSSControl(t, a2, map[string]any{"type": "DELIVERY_STATUS", "requestId": "query2", "ruleId": ruleID, "messageId": env.MessageID})
	if reply := readWSSControl(t, a2); reply["type"] != "DELIVERY_RESULT" || reply["status"] != "APPLIED" {
		t.Fatalf("terminal ACK lost on source reconnect: %v", reply)
	}
	sendWSSControl(t, a2, map[string]any{"type": "SEQUENCE_CURSOR", "requestId": "cursor1", "ruleId": ruleID})
	if reply := readWSSControl(t, a2); reply["lastSequence"] != float64(1) {
		t.Fatalf("accepted sequence cursor lost on reconnect: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{"type": "DELIVERY_STATUS", "requestId": "unauthorized", "ruleId": ruleID, "messageId": env.MessageID})
	if reply := readWSSControl(t, b); reply["type"] != "RULE_ERROR" {
		t.Fatalf("receiver read source-only delivery result: %v", reply)
	}
}

func TestWSSLargeEncryptedSnapshotCrossesOldFrameLimit(t *testing.T) {
	store := testBrowserStore(t)
	source, key := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	ctx := context.Background()
	if err := store.OfferRule(ctx, source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(ctx, target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(ctx, source.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, key)
	b := dialApprovedBrowser(t, server, target, targetKey)
	// coder/websocket clients default to a smaller inbound read limit.
	b.SetReadLimit(1536 * 1024)
	env := signedTestSnapshot(t, key, source.ID, target.ID, ruleID)
	cipher := make([]byte, 550*1024)
	if _, err := rand.Read(cipher); err != nil {
		t.Fatal(err)
	}
	env.Ciphertext = base64.RawURLEncoding.EncodeToString(cipher)
	digest := sha256.Sum256([]byte(protocol.SignedHeader(env) + "\n" + env.Ciphertext))
	rSig, sSig, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	rSig.FillBytes(sig[:32])
	sSig.FillBytes(sig[32:])
	env.Signature = base64.RawURLEncoding.EncodeToString(sig)
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "big",
		"envelope": env})
	if resp := readWSSControl(t, a); resp["status"] != "RELAYED" {
		t.Fatalf("large signed session not relayed: %v", resp["type"])
	}
	if resp := readWSSControl(t, b); resp["type"] != "SESSION_SNAPSHOT" {
		t.Fatalf("large signed session not delivered: %v", resp["type"])
	}
}

// TestWSSOnlyAuthenticatesTheOriginBoundToAnApprovedDevice proves that an
// auto-recorded extension Origin is not an authorization grant. Even a valid
// device signature cannot be replayed from an unrelated extension Origin.
func TestWSSOnlyAuthenticatesTheOriginBoundToAnApprovedDevice(t *testing.T) {
	store := testBrowserStore(t)
	key, signing := makeKey(t)
	_, encryption := makeKey(t)
	device := BrowserDevice{
		ID:            "browser_" + uuid.NewString(),
		Name:          "Origin bound Chrome",
		SigningKey:    signing,
		EncryptionKey: encryption,
	}
	ctx := context.Background()
	saved, _, err := store.EnrollExtensionDevice(ctx, testExtensionID, device)
	if err != nil || saved.State != "pending" || saved.Send || saved.Receive {
		t.Fatalf("enrollment granted access: %+v %v", saved, err)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, nil))
	defer server.Close()
	authorized := dialApprovedBrowser(t, server, device, key)
	sendWSSControl(t, authorized, map[string]any{"type": "LIST_PEERS", "requestId": "good"})
	if result := readWSSControl(t, authorized); result["type"] != "PEERS" {
		t.Fatalf("approved device not connected through its recorded Origin: %v", result)
	}

	wrongOrigin := "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	url := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/api/v1/browser-sync/ws"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: server.Client(),
		HTTPHeader: http.Header{"Origin": []string{wrongOrigin}},
	})
	if err != nil {
		t.Fatalf("well-formed Origin should reach device authentication: %v", err)
	}
	defer conn.CloseNow()
	sendWSSControl(t, conn, map[string]any{"type": "AUTH_HELLO", "deviceId": device.ID})
	challenge := readWSSControl(t, conn)
	nonce, ok := challenge["challenge"].(string)
	if !ok {
		t.Fatalf("missing challenge: %v", challenge)
	}
	sendWSSControl(t, conn, map[string]any{
		"type": "AUTH_PROOF", "deviceId": device.ID, "challenge": nonce,
		"signature": signatureFor(t, key, server.URL, device.ID, nonce),
	})
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err = conn.Read(readCtx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("another extension Origin received device authorization: %v", err)
	}
}

func TestWSSRevokedDeviceCannotContinueAuthenticatedSession(t *testing.T) {
	store := testBrowserStore(t)
	key, signing := makeKey(t)
	_, encryption := makeKey(t)
	device := BrowserDevice{
		ID: "browser_" + uuid.NewString(), Name: "Revocable Chrome",
		SigningKey: signing, EncryptionKey: encryption,
	}
	ctx := context.Background()
	if _, _, err := store.EnrollExtensionDevice(ctx, testExtensionID, device); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, nil))
	defer server.Close()
	conn := dialApprovedBrowser(t, server, device, key)
	if err := store.Revoke(ctx, device.ID); err != nil {
		t.Fatal(err)
	}
	sendWSSControl(t, conn, map[string]any{"type": "PING"})
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("revoked browser kept its WSS session: %v", err)
	}
}

func TestWSSRevokedOriginRejectsExistingDeviceSession(t *testing.T) {
	store := testBrowserStore(t)
	key, signing := makeKey(t)
	_, encryption := makeKey(t)
	device := BrowserDevice{
		ID: "browser_" + uuid.NewString(), Name: "Origin revocation browser",
		SigningKey: signing, EncryptionKey: encryption,
	}
	ctx := context.Background()
	if _, _, err := store.EnrollExtensionDevice(ctx, testExtensionID, device); err != nil {
		t.Fatal(err)
	}
	if err := store.ApproveEnrollment(ctx, device.ID, "id_1", true, false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, nil))
	defer server.Close()
	conn := dialApprovedBrowser(t, server, device, key)
	if err := store.SetExtensionApproved(ctx, testExtensionID, false); err != nil {
		t.Fatal(err)
	}
	sendWSSControl(t, conn, map[string]any{"type": "PING"})
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := conn.Read(readCtx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("revoked Origin kept its WSS session: %v", err)
	}
}

func TestWSSReceiverDiagnosticCodeIsValidatedAndAudited(t *testing.T) {
	store := testBrowserStore(t)
	source, sourceKey := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	ctx := context.Background()
	if err := store.OfferRule(ctx, source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(ctx, target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(ctx, source.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, sourceKey)
	b := dialApprovedBrowser(t, server, target, targetKey)
	env := signedTestSnapshot(t, sourceKey, source.ID, target.ID, ruleID)
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT", "requestId": "send", "envelope": env})
	if reply := readWSSControl(t, a); reply["status"] != "RELAYED" {
		t.Fatalf("source snapshot was not relayed: %v", reply)
	}
	if reply := readWSSControl(t, b); reply["type"] != "SESSION_SNAPSHOT" {
		t.Fatalf("receiver did not receive encrypted session: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_ACK", "requestId": "bad", "ruleId": ruleID,
		"messageId": env.MessageID, "status": "FAILED",
		"errorCode": "session_cookie=secret-value",
	})
	if reply := readWSSControl(t, b); reply["type"] != "RULE_ERROR" {
		t.Fatalf("arbitrary diagnostic text was accepted: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_ACK", "requestId": "good", "ruleId": ruleID,
		"messageId": env.MessageID, "status": "FAILED",
		"errorCode": "INDEXEDDB_FAILURE",
	})
	if reply := readWSSControl(t, a); reply["type"] != "SYNC_ACK" || reply["status"] != "FAILED" {
		t.Fatalf("source did not receive terminal failure: %v", reply)
	}
	if reply := readWSSControl(t, b); reply["type"] != "SYNC_STATUS" {
		t.Fatalf("receiver failure receipt did not commit: %v", reply)
	}
	events, err := store.AdminAuditFiltered(ctx, 0, 10, "delivery_result", source.ID)
	if err != nil || len(events) != 1 || events[0].ErrorCode != "INDEXEDDB_FAILURE" {
		t.Fatalf("receiver failure diagnostic missing from safe audit: %+v %v", events, err)
	}
}

func TestWSSReceiverTransferRequestTokenIsRoutedAndSigned(t *testing.T) {
	store := testBrowserStore(t)
	source, sourceKey := approvedWSSBrowser(t, store, true, false)
	target, targetKey := approvedWSSBrowser(t, store, false, true)
	ruleID := uuid.NewString()
	ctx := context.Background()
	if err := store.OfferRule(ctx, source.ID, encryptedTestOffer(ruleID, target.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptRule(ctx, target.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmRule(ctx, source.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(NewHandler(store, []string{testExtensionID}))
	defer server.Close()
	a := dialApprovedBrowser(t, server, source, sourceKey)
	b := dialApprovedBrowser(t, server, target, targetKey)
	token := uuid.NewString()
	sendWSSControl(t, b, map[string]any{
		"type": "SYNC_REQUEST", "requestId": "request-one",
		"ruleId": ruleID, "transferRequestId": token,
	})
	forward := readWSSControl(t, a)
	if forward["type"] != "SYNC_REQUEST" || forward["transferRequestId"] != token {
		t.Fatalf("authenticated B transfer token not forwarded to A: %v", forward)
	}
	if reply := readWSSControl(t, b); reply["status"] != "REQUESTED" {
		t.Fatalf("B request status missing: %v", reply)
	}
	envelope := signedTestSnapshot(t, sourceKey, source.ID, target.ID, ruleID)
	envelope.TransferRequestID = token
	hash := sha256.Sum256([]byte(protocol.SignedHeader(envelope) + "\n" + envelope.Ciphertext))
	rVal, sVal, err := ecdsa.Sign(rand.Reader, sourceKey, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	rVal.FillBytes(signature[:32])
	sVal.FillBytes(signature[32:])
	envelope.Signature = base64.RawURLEncoding.EncodeToString(signature)
	// Even a fully authenticated source cannot swap the receiver's token
	// without invalidating its signature. Rejected frames use no sequence.
	swapped := envelope
	swapped.TransferRequestID = uuid.NewString()
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT",
		"requestId": "tamper", "envelope": swapped})
	if reply := readWSSControl(t, a); reply["type"] != "RULE_ERROR" {
		t.Fatalf("tampered receiver request token accepted: %v", reply)
	}
	sendWSSControl(t, a, map[string]any{"type": "SESSION_SNAPSHOT",
		"requestId": "snapshot-one", "envelope": envelope})
	if reply := readWSSControl(t, a); reply["status"] != "RELAYED" {
		t.Fatalf("signed snapshot with request token rejected: %v", reply)
	}
	received := readWSSControl(t, b)
	routed, ok := received["envelope"].(map[string]any)
	if !ok || routed["transferRequestId"] != token {
		t.Fatalf("request token missing from encrypted delivery: %v", received)
	}
	sendWSSControl(t, b, map[string]any{"type": "SYNC_ACK",
		"requestId": "ack-one", "ruleId": ruleID,
		"messageId": envelope.MessageID, "status": "APPLIED"})
	if reply := readWSSControl(t, a); reply["status"] != "APPLIED" {
		t.Fatalf("source missing final ACK: %v", reply)
	}
	if reply := readWSSControl(t, b); reply["status"] != "ACK_FORWARDED" {
		t.Fatalf("receiver missing ACK status: %v", reply)
	}
	sendWSSControl(t, b, map[string]any{"type": "SYNC_REQUEST", "requestId": "bad",
		"ruleId": ruleID, "transferRequestId": "malformed-token"})
	if reply := readWSSControl(t, b); reply["type"] != "RULE_ERROR" {
		t.Fatalf("malformed transfer token was allowed: %v", reply)
	}
}
