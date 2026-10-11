package browsersync

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	protocol "relayproxy/internal/browser_sync"
)

// Handler authenticates each browser before accepting encrypted transfers.
// Resource quotas are in addition to per-frame protocol validation.
type Handler struct {
	Store      *Store
	auth       *Authenticator
	extensions map[string]struct{}
	mu         sync.Mutex
	attempts   map[string]attempt
	slots      chan struct{}
	limits     *browserLimiter
	clientMu   sync.RWMutex
	clients    map[string]*browserConnection
	network    *NetworkProxy
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
		attempts: map[string]attempt{}, slots: make(chan struct{}, 64), limits: newBrowserLimiter(),
		clients: make(map[string]*browserConnection)}
}

// ConfigureNetworkProxy enables the relay-shared authenticated browser-to-Agent
// CONNECT service. With nil the entire network-binding control plane is denied.
func (h *Handler) ConfigureNetworkProxy(proxy *NetworkProxy) { h.network = proxy }

func (h *Handler) allowOrigin(w http.ResponseWriter, r *http.Request, enrollment bool) bool {
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
	id := strings.TrimPrefix(origin, "chrome-extension://")
	if !chromeExtensionIDPattern.MatchString(id) {
		http.Error(w, "invalid Chrome extension Origin", http.StatusForbidden)
		return false
	}
	if !enrollment {
		// Administrator revocation always overrides legacy static allowlists.
		if h.Store.ExtensionRevoked(r.Context(), id) {
			http.Error(w, "extension revoked by administrator", http.StatusForbidden)
			return false
		}
		if _, ok := h.extensions[origin]; !ok && !h.Store.ExtensionApproved(r.Context(), id) {
			http.Error(w, "extension not approved by administrator", http.StatusForbidden)
			return false
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enrollment := r.URL.Path == "/api/v1/browser-sync/extensions/register" &&
		(r.Method == http.MethodPost || r.Method == http.MethodOptions)
	if !h.allowOrigin(w, r, enrollment) {
		return
	}
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case enrollment:
		if !h.permitRegistration(r) {
			http.Error(w, "extension enrollment rate limit", http.StatusTooManyRequests)
			return
		}
		id := strings.TrimPrefix(r.Header.Get("Origin"), "chrome-extension://")
		if r.ContentLength != 0 {
			// Modern Chrome clients enroll extension Origin and device keys in one
			// request. This is NOT an approval: neither identity nor capabilities
			// can be supplied, and both remain pending until an admin approves.
			h.registerExtensionDevice(w, r, id)
			return
		}
		// Preserve extension-only enrollment for older development clients.
		if _, trusted := h.extensions[r.Header.Get("Origin")]; trusted {
			writeBrowserJSON(w, http.StatusAccepted, map[string]string{"id": id, "state": "approved"})
			return
		}
		state, err := h.Store.RequestExtension(r.Context(), id)
		if err != nil {
			http.Error(w, "extension enrollment unavailable", http.StatusServiceUnavailable)
			return
		}
		writeBrowserJSON(w, http.StatusAccepted, map[string]string{"id": id, "state": state})
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

// readRegistration refuses any requested capability or client-selected identity.
func readRegistration(w http.ResponseWriter, r *http.Request) (BrowserDevice, bool) {
	var d BrowserDevice
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		http.Error(w, "invalid browser registration", http.StatusBadRequest)
		return BrowserDevice{}, false
	}
	if d.State != "" || d.IdentityID != "" || d.Send || d.Receive ||
		!d.CreatedAt.IsZero() || d.ExtensionID != "" || d.ExtensionState != "" {
		http.Error(w, "browser privileges cannot be requested during enrollment", http.StatusForbidden)
		return BrowserDevice{}, false
	}
	if err := ValidateRegistration(d); err != nil {
		http.Error(w, "invalid device signing or encryption public key", http.StatusBadRequest)
		return BrowserDevice{}, false
	}
	return d, true
}

func (h *Handler) registerExtensionDevice(w http.ResponseWriter, r *http.Request, extensionID string) {
	d, ok := readRegistration(w, r)
	if !ok {
		return
	}
	saved, state, err := h.Store.EnrollExtensionDevice(r.Context(), extensionID, d)
	if err != nil {
		if errors.Is(err, ErrDeviceConflict) {
			http.Error(w, "registered identity key or extension mismatch", http.StatusConflict)
			return
		}
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "revoked extension cannot request enrollment", http.StatusForbidden)
			return
		}
		http.Error(w, "browser enrollment unavailable", http.StatusServiceUnavailable)
		return
	}
	writeBrowserJSON(w, http.StatusAccepted, map[string]string{
		"id": saved.ID, "state": saved.State, "extensionId": extensionID, "extensionState": state,
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.permitRegistration(r) {
		http.Error(w, "enrollment rate limit", http.StatusTooManyRequests)
		return
	}
	d, ok := readRegistration(w, r)
	if !ok {
		return
	}
	extensionID := strings.TrimPrefix(r.Header.Get("Origin"), "chrome-extension://")
	saved, state, err := h.Store.EnrollExtensionDevice(r.Context(), extensionID, d)
	if err != nil {
		if errors.Is(err, ErrDeviceConflict) {
			http.Error(w, "registered identity key or extension mismatch", http.StatusConflict)
			return
		}
		http.Error(w, "registration failed", http.StatusInternalServerError)
		return
	}
	writeBrowserJSON(w, http.StatusAccepted, map[string]string{
		"id": saved.ID, "state": saved.State, "extensionId": extensionID, "extensionState": state,
	})
}

type authFrame struct {
	Type      string `json:"type"`
	DeviceID  string `json:"deviceId"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
}

// browserRequestOrigin preserves the exact non-default TCP port while
// matching Chrome URL.origin serialization for the default HTTPS port.
func browserRequestOrigin(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ":443")
	return "https://" + host
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
	// Bound unauthenticated challenge attempts by the direct TCP peer, not
	// spoofable X-Forwarded-For. Slots alone cannot stop repeated challenges.
	remote, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil || !h.limits.allow("hello:"+remote, 40, time.Minute) {
		_ = conn.Close(websocket.StatusPolicyViolation, "authentication rate limited")
		return
	}
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
	// Signature verification is bound to the HTTPS server origin. Extension
	// approval is bound separately to the actual chrome-extension:// Origin.
	extensionOrigin := r.Header.Get("Origin")
	// Chrome URL.origin canonicalizes HTTPS's default port 443 away.
	// Host headers coming through a proxy may still contain an explicit :443.
	// Both must produce the same signed origin or approved browsers fail AUTH_PROOF.
	origin := browserRequestOrigin(r.Host)
	device, err := h.auth.Verify(proofCtx, proof.DeviceID, origin, nonce, proof.Signature)
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "not authorized")
		return
	}
	if !h.limits.allow("connect:"+device.ID, 12, time.Minute) {
		_ = conn.Close(websocket.StatusPolicyViolation, "reconnect rate limited")
		return
	}
	// The browser must be discoverable as an online recipient by the time it
	// receives AUTH_OK, otherwise a peer can immediately race the registration.
	client := &browserConnection{conn: conn}
	h.attach(device.ID, client)
	defer h.detach(device.ID, client)
	if err := client.send(proofCtx, map[string]any{
		"type": "AUTH_OK", "deviceId": device.ID, "send": device.Send, "receive": device.Receive,
		"sessionTransferEnabled": true,
	}); err != nil {
		return
	}
	// All session envelopes are E2EE and signed. No Cookie plaintext reaches Server.
	for {
		loopCtx, done := context.WithTimeout(ctx, 55*time.Second)
		var msg ruleControlFrame
		err := readFrameLimit(loopCtx, conn, &msg, 1536*1024)
		done()
		if err != nil {
			return
		}
		current, err := h.Store.deviceAllowed(ctx, device.ID)
		originID := strings.TrimPrefix(extensionOrigin, "chrome-extension://")
		_, legacy := h.extensions[extensionOrigin]
		if err != nil || current.ID != device.ID || h.Store.ExtensionRevoked(ctx, originID) ||
			(!legacy && !h.Store.ExtensionApproved(ctx, originID)) {
			_ = conn.Close(websocket.StatusPolicyViolation, "browser approval revoked")
			return
		}
		if len(msg.Type) > 48 || len(msg.RequestID) > 64 ||
			len(msg.RuleID) > 128 || len(msg.MessageID) > 128 {
			_ = conn.Close(websocket.StatusPolicyViolation, "oversized browser control identifier")
			return
		}
		if !h.permitControl(device.ID, msg) {
			if err := client.send(ctx, map[string]any{"type": "RULE_ERROR", "requestId": msg.RequestID,
				"error": "browser sync rate limit exceeded"}); err != nil {
				return
			}
			continue
		}
		var payload any
		switch msg.Type {
		case "PING":
			payload = map[string]any{"type": "PONG"}
		case "DELIVERY_STATUS":
			status, err := h.Store.DeliveryStatus(ctx, device.ID, msg.RuleID, msg.MessageID, time.Now().UTC())
			if err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "DELIVERY_RESULT", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "messageId": msg.MessageID, "status": status}
			}
		case "SEQUENCE_CURSOR":
			cursor, err := h.Store.SessionCursor(ctx, device.ID, msg.RuleID)
			if err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "SESSION_CURSOR", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "lastSequence": cursor}
			}
		case "SESSION_SNAPSHOT":
			if err := h.relaySnapshot(ctx, device.ID, msg.Envelope); err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "SYNC_STATUS", "requestId": msg.RequestID, "status": "RELAYED"}
			}
		case "SYNC_REQUEST":
			if err := h.requestSnapshot(ctx, device.ID, msg.RuleID); err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "SYNC_STATUS", "requestId": msg.RequestID, "status": "REQUESTED"}
			}
		case "SYNC_ACK":
			if err := h.relayAcknowledgement(ctx, device.ID, msg.RuleID, msg.MessageID, msg.Status); err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "SYNC_STATUS", "requestId": msg.RequestID, "status": "ACK_FORWARDED"}
			}
		case "LIST_PEERS":
			peers, err := h.Store.PeerList(ctx, device.ID)
			if err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "PEERS", "requestId": msg.RequestID, "peers": peers}
			}
		case "LIST_RULES":
			rules, err := h.Store.ListRules(ctx, device.ID)
			if err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "RULES", "requestId": msg.RequestID, "rules": rules}
			}
		case "NETWORK_EXITS":
			if h.network == nil {
				payload = networkRuleError(msg.RequestID, "NETWORK_DISABLED",
					"Browser Sync 未启用或 TCP 中继 TLS 不可用；请管理员检查 Browser Sync 与证书配置")
			} else if !current.Send {
				payload = networkRuleError(msg.RequestID, "SOURCE_SEND_REQUIRED",
					"当前来源浏览器未获 send 权限；请管理员检查浏览器设备授权")
			} else {
				payload = map[string]any{"type": "NETWORK_EXITS", "requestId": msg.RequestID,
					"exits": h.network.AvailableExits(ctx, device.ID)}
			}
		case "NETWORK_OFFER":
			switch {
			case h.network == nil:
				payload = networkRuleError(msg.RequestID, "NETWORK_DISABLED",
					"Browser Sync 网络代理不可用；需启用 Browser Sync 和 TCP 中继 TLS")
			case !current.Send:
				payload = networkRuleError(msg.RequestID, "SOURCE_SEND_REQUIRED",
					"当前来源浏览器未获 send 权限")
			default:
				// Resolve the precise reason for this authenticated source instead
				// of conflating configuration, exit permissions and pairing errors.
				binding, bindErr := h.Store.NetworkBinding(ctx, device.ID, msg.RuleID)
				switch {
				case bindErr != nil:
					payload = networkRuleError(msg.RequestID, "RULE_INACTIVE",
						"会话配对尚未激活、双端授权不完整或规则已失效；请刷新并检查配对状态")
				case binding.Status != "none":
					payload = networkRuleError(msg.RequestID, "BINDING_EXISTS",
						"此配对已经创建网络绑定；请刷新查看状态，撤销后需重新配对才能更换出口")
				case !h.network.AllowedExit(current.IdentityID, msg.ExitDeviceID):
					payload = networkRuleError(msg.RequestID, "EXIT_NOT_ALLOWED",
						"Agent 出口不可用：检查在线状态、proxy.exit 授权、身份一致性及 Agent 本机浏览器出口共享开关")
				case h.Store.OfferNetworkBinding(ctx, device.ID, msg.RuleID, msg.Host, msg.ExitDeviceID) != nil:
					payload = networkRuleError(msg.RequestID, "NETWORK_OFFER_DENIED",
						"网络绑定未创建：请确认 HTTPS 443 域名有效、接收端具备 receive 权限且与来源端身份一致")
				default:
					payload = map[string]any{"type": "NETWORK_UPDATED", "requestId": msg.RequestID,
						"ruleId": msg.RuleID, "status": "offered"}
				}
			}
		case "NETWORK_BINDING":
			// Existing Cookie-only sessions must work when the optional
			// browser network proxy is unavailable. A stored active binding is
			// still reported so the receiver keeps its fail-closed guard.
			binding, err := h.Store.NetworkBinding(ctx, device.ID, msg.RuleID)
			if err != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "NETWORK_BINDING", "requestId": msg.RequestID, "binding": binding}
			}
		case "NETWORK_ACCEPT":
			if h.network == nil || !current.Receive ||
				h.Store.AcceptNetworkBinding(ctx, device.ID, msg.RuleID, msg.Host) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "NETWORK_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "status": "active"}
			}
		case "NETWORK_REVOKE":
			if h.Store.RevokeNetworkBinding(ctx, device.ID, msg.RuleID) != nil {
				payload = ruleError(msg.RequestID)
			} else {
				payload = map[string]any{"type": "NETWORK_UPDATED", "requestId": msg.RequestID,
					"ruleId": msg.RuleID, "status": "revoked"}
			}
		case "NETWORK_TOKEN":
			if h.network == nil || !current.Receive {
				payload = ruleError(msg.RequestID)
			} else {
				result, err := h.network.Issue(ctx, device.ID)
				if err != nil {
					payload = ruleError(msg.RequestID)
				} else {
					result["requestId"] = msg.RequestID
					payload = result
				}
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
		err = client.send(ctx, payload)
		if err != nil {
			return
		}
	}
}

type ruleControlFrame struct {
	Type         string             `json:"type"`
	RequestID    string             `json:"requestId"`
	RuleID       string             `json:"ruleId"`
	Offer        *EncryptedOffer    `json:"offer"`
	Envelope     *protocol.Envelope `json:"envelope"`
	MessageID    string             `json:"messageId"`
	Status       string             `json:"status"`
	Host         string             `json:"host"`
	ExitDeviceID string             `json:"exitDeviceId"`
}

// Codes are fixed, non-secret operator diagnostics, only returned on an
// already authenticated Browser Sync WebSocket. Cookie values are never logged.
func networkRuleError(requestID, code, message string) map[string]any {
	return map[string]any{"type": "RULE_ERROR", "requestId": requestID,
		"code": code, "error": message}
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
