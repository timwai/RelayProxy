package browsersync

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Handler deliberately does NOT forward SESSION_SNAPSHOT yet: no secret
// traffic can flow until mutual rule approval and E2EE are implemented.
type Handler struct {
	Store      *Store
	auth       *Authenticator
	extensions map[string]struct{}
	mu         sync.Mutex
	attempts   map[string]attempt
	slots      chan struct{}
}
type attempt struct {
	window time.Time
	count  int
}

func NewHandler(store *Store, extensionIDs []string) *Handler {
	allowed := map[string]struct{}{}
	for _, id := range extensionIDs {
		allowed["chrome-extension://"+id] = struct{}{}
	}
	return &Handler{Store: store, auth: NewAuthenticator(store), extensions: allowed,
		attempts: map[string]attempt{}, slots: make(chan struct{}, 64)}
}

func (h *Handler) allowOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.TLS == nil {
		http.Error(w, "HTTPS required", http.StatusUpgradeRequired)
		return false
	}
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "chrome-extension" || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		http.Error(w, "browser extension Origin required", http.StatusForbidden)
		return false
	}
	if _, ok := h.extensions[origin]; !ok {
		http.Error(w, "extension Origin is not configured", http.StatusForbidden)
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.allowOrigin(w, r) {
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case r.URL.Path == "/api/v1/browser-sync/devices/register" && r.Method == http.MethodPost:
		h.register(w, r)
	case r.URL.Path == "/api/v1/browser-sync/ws" && r.Method == http.MethodGet:
		h.connect(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) permitRegistration(r *http.Request) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	// RemoteAddr is the direct TCP peer: do not trust forwarded headers.
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for ip, a := range h.attempts {
		if now.Sub(a.window) > 15*time.Minute {
			delete(h.attempts, ip)
		}
	}
	if len(h.attempts) > 10000 {
		return false
	}
	a := h.attempts[remote]
	if now.Sub(a.window) > 10*time.Minute {
		a = attempt{window: now}
	}
	a.count++
	h.attempts[remote] = a
	return a.count <= 15
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.permitRegistration(r) {
		http.Error(w, "enrollment rate limit", http.StatusTooManyRequests)
		return
	}
	var d BrowserDevice
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		http.Error(w, "invalid browser registration", http.StatusBadRequest)
		return
	}
	// The request may not supply approval, identities, or capabilities.
	if d.State != "" || d.IdentityID != "" || d.Send || d.Receive || !d.CreatedAt.IsZero() {
		http.Error(w, "browser privileges cannot be requested during enrollment", http.StatusForbidden)
		return
	}
	if err := ValidateRegistration(d); err != nil {
		http.Error(w, "invalid device signing or encryption public key", http.StatusBadRequest)
		return
	}
	saved, err := h.Store.Register(r.Context(), d)
	if err != nil {
		if errors.Is(err, ErrDeviceConflict) {
			http.Error(w, "registered identity key mismatch", http.StatusConflict)
			return
		}
		http.Error(w, "registration failed", http.StatusInternalServerError)
		return
	}
	writeBrowserJSON(w, http.StatusAccepted, map[string]string{"id": saved.ID, "state": saved.State})
}

type authFrame struct {
	Type      string `json:"type"`
	DeviceID  string `json:"deviceId"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		http.Error(w, "browser connections full", http.StatusServiceUnavailable)
		return
	}
	// The Admin listener has short normal HTTP read/write timeouts.
	// Clear deadlines only for the authenticated-origin WebSocket upgrade.
	// The proof is still strictly bounded by the 15-second auth context.
	control := http.NewResponseController(w)
	if err := control.SetReadDeadline(time.Time{}); err != nil {
		http.Error(w, "WebSocket deadline control unavailable", http.StatusInternalServerError)
		return
	}
	if err := control.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "WebSocket deadline control unavailable", http.StatusInternalServerError)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin verified by allowOrigin BEFORE upgrading
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")
	ctx := r.Context()
	// A WS connection has no privileges prior to a valid, single-use proof.
	proofCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Must first learn the ID to issue a scoped challenge. This is not auth.
	var hello authFrame
	if err := readFrame(proofCtx, conn, &hello); err != nil || hello.Type != "AUTH_HELLO" {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid hello")
		return
	}
	nonce, err := h.auth.Challenge(hello.DeviceID)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid identity")
		return
	}
	if err := conn.Write(proofCtx, websocket.MessageText, mustJSON(map[string]any{
		"type": "AUTH_CHALLENGE", "deviceId": hello.DeviceID, "challenge": nonce,
	})); err != nil {
		return
	}
	var proof authFrame
	if err := readFrame(proofCtx, conn, &proof); err != nil || proof.Type != "AUTH_PROOF" ||
		proof.DeviceID != hello.DeviceID || proof.Challenge != nonce {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid proof")
		return
	}
	origin := "https://" + r.Host
	device, err := h.auth.Verify(proofCtx, proof.DeviceID, origin, nonce, proof.Signature)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "not authorized")
		return
	}
	if err := conn.Write(proofCtx, websocket.MessageText, mustJSON(map[string]any{
		"type": "AUTH_OK", "deviceId": device.ID, "send": device.Send, "receive": device.Receive,
		"sessionTransferEnabled": false,
	})); err != nil {
		return
	}

	// Control-plane only. Encrypted offers carry policy metadata, not
	// browser credentials. SESSION_* frames remain explicitly forbidden.
	for {
		loopCtx, done := context.WithTimeout(ctx, 55*time.Second)
		var msg ruleControlFrame
		err := readFrameLimit(loopCtx, conn, &msg, 16*1024)
		done()
		if err != nil {
			return
		}
		current, err := h.Store.deviceAllowed(ctx, device.ID)
		if err != nil || current.ID != device.ID {
			_ = conn.Close(websocket.StatusPolicyViolation, "browser approval revoked")
			return
		}
		var payload any
		switch msg.Type {
		case "PING":
			payload = map[string]any{"type": "PONG"}
		case "LIST_PEERS":
			peers, err := h.Store.PeerList(ctx, device.ID)
			if err != nil { payload = ruleError(msg.RequestID) } else {
				payload = map[string]any{"type": "PEERS", "requestId": msg.RequestID, "peers": peers}
			}
		case "LIST_RULES":
			rules, err := h.Store.ListRules(ctx, device.ID)
			if err != nil { payload = ruleError(msg.RequestID) } else {
				payload = map[string]any{"type": "RULES", "requestId": msg.RequestID, "rules": rules}
			}
		case "RULE_OFFER":
			if msg.Offer == nil || !current.Send ||
				h.Store.OfferRule(ctx, device.ID, *msg.Offer) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "RULE_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.Offer.RuleID, "status": "offered"}
			}
		case "RULE_ACCEPT":
			if !current.Receive || h.Store.AcceptRule(ctx, device.ID, msg.RuleID) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "RULE_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "status": "target_approved"}
			}
		case "RULE_CONFIRM":
			if !current.Send || h.Store.ConfirmRule(ctx, device.ID, msg.RuleID) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "RULE_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "status": "active"}
			}
		case "RULE_REVOKE":
			if h.Store.RevokeRule(ctx, device.ID, msg.RuleID) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "RULE_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "status": "revoked"}
			}
		default:
			_ = conn.Close(websocket.StatusPolicyViolation, "unsupported browser control event")
			return
		}
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		err = conn.Write(writeCtx, websocket.MessageText, mustJSON(payload))
		stop()
		if err != nil {
			return
		}
	}
}

type ruleControlFrame struct {
	Type      string          `json:"type"`
	RequestID string          `json:"requestId"`
	RuleID    string          `json:"ruleId"`
	Offer     *EncryptedOffer `json:"offer"`
}

func ruleError(requestID string) map[string]any {
	return map[string]any{"type": "RULE_ERROR", "requestId": requestID, "error": "rule not authorized or invalid"}
}
func readFrame(ctx context.Context, c *websocket.Conn, out any) error {
	return readFrameLimit(ctx, c, out, 4096)
}

func readFrameLimit(ctx context.Context, c *websocket.Conn, out any, limit int) error {
	c.SetReadLimit(int64(limit))
	typ, raw, err := c.Read(ctx)
	if err != nil {
		return err
	}
	if typ != websocket.MessageText || len(raw) > limit {
		return errors.New("invalid browser control frame")
	}
	return json.Unmarshal(raw, out)
}
func writeBrowserJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func mustJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
