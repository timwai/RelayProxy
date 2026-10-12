package browsersync

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	protocol "relayproxy/internal/browser_sync"
)

type browserConnection struct {
	conn        *websocket.Conn
	connectedAt time.Time
	mu          sync.Mutex
}

func (c *browserConnection) send(ctx context.Context, message any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.conn.Write(timeout, websocket.MessageText, mustJSON(message))
}

func (h *Handler) attach(id string, c *browserConnection) {
	h.clientMu.Lock()
	old := h.clients[id]
	h.clients[id] = c
	h.clientMu.Unlock()
	h.notifyPresence()
	if old != nil && old != c {
		_ = old.conn.Close(websocket.StatusPolicyViolation, "browser reconnected")
	}
}
func (h *Handler) detach(id string, c *browserConnection) {
	h.clientMu.Lock()
	removed := h.clients[id] == c
	if removed {
		delete(h.clients, id)
	}
	h.clientMu.Unlock()
	if removed {
		h.notifyPresence()
	}
}

// DisconnectDevice terminates any authenticated websocket still attached to
// a revoked/deleted browser before its old credentials can be reused.
func (h *Handler) DisconnectDevice(id string) {
	h.clientMu.Lock()
	c := h.clients[id]
	delete(h.clients, id)
	h.clientMu.Unlock()
	if c != nil {
		h.notifyPresence()
		_ = c.conn.Close(websocket.StatusPolicyViolation, "browser authorization revoked")
	}
}

// OnlineSnapshot is a transient observation of authenticated WSS sessions.
// Only admin handlers call it; no keys or browser messages are exposed.
func (h *Handler) OnlineSnapshot() map[string]time.Time {
	h.clientMu.RLock()
	defer h.clientMu.RUnlock()
	out := make(map[string]time.Time, len(h.clients))
	for id, client := range h.clients {
		out[id] = client.connectedAt
	}
	return out
}

func (h *Handler) destination(id string) *browserConnection {
	h.clientMu.RLock()
	defer h.clientMu.RUnlock()
	return h.clients[id]
}

func (h *Handler) relaySnapshot(ctx context.Context, senderID string, e *protocol.Envelope) error {
	if e == nil {
		return errors.New("missing encrypted snapshot")
	}
	if err := h.Store.ValidateSignedSession(ctx, senderID, *e, time.Now().UTC()); err != nil {
		return err
	}
	target := h.destination(e.TargetBrowserDeviceID)
	if target == nil {
		return errors.New("recipient browser offline")
	}
	if err := h.Store.AdvanceSessionSequence(ctx, e.RuleID, e.Sequence); err != nil {
		return err
	}
	if err := h.Store.RecordDelivery(ctx, e.MessageID, e.RuleID,
		e.SourceBrowserDeviceID, e.TargetBrowserDeviceID, time.Now().UTC()); err != nil {
		return err
	}
	if err := target.send(ctx, map[string]any{"type": "SESSION_SNAPSHOT", "envelope": e}); err != nil {
		h.Store.DiscardDelivery(ctx, e.MessageID)
		return err
	}
	return nil
}

func (h *Handler) requestSnapshot(ctx context.Context, receiverID, ruleID string) error {
	return h.requestSnapshotWithToken(ctx, receiverID, ruleID, "")
}

func (h *Handler) requestSnapshotWithToken(ctx context.Context, receiverID, ruleID, transferRequestID string) error {
	if transferRequestID != "" {
		if len(transferRequestID) != 36 {
			return ErrRuleDenied
		}
		if _, err := uuid.Parse(transferRequestID); err != nil {
			return ErrRuleDenied
		}
	}
	receiver, err := h.Store.deviceAllowed(ctx, receiverID)
	if err != nil || !receiver.Receive {
		return ErrRuleDenied
	}
	rule, err := h.Store.ActiveRule(ctx, ruleID)
	if err != nil || !rule.Active || !rule.SourceApproved || !rule.TargetApproved ||
		!rule.KeyFingerprintsVerified || rule.TargetBrowserDeviceID != receiverID {
		return ErrRuleDenied
	}
	source, err := h.Store.deviceAllowed(ctx, rule.SourceBrowserDeviceID)
	if err != nil || !source.Send || source.IdentityID != receiver.IdentityID {
		return ErrRuleDenied
	}
	target := h.destination(source.ID)
	if target == nil {
		return errors.New("source browser offline")
	}
	return target.send(ctx, map[string]any{"type": "SYNC_REQUEST", "ruleId": ruleID,
		"transferRequestId": transferRequestID})
}

func (h *Handler) relayAcknowledgement(ctx context.Context, receiverID, ruleID, messageID, status string) error {
	return h.relayAcknowledgementWithCode(ctx, receiverID, ruleID, messageID, status, "")
}

func (h *Handler) relayAcknowledgementWithCode(ctx context.Context, receiverID, ruleID, messageID, status, code string) error {
	switch status {
	case "RECEIVED", "APPLIED", "FAILED", "CONFLICT":
	default:
		return ErrRuleDenied
	}
	sourceID, err := h.Store.ClaimDeliveryReceiptWithCode(ctx, receiverID, ruleID, messageID, status, code, time.Now().UTC())
	if err != nil {
		return err
	}
	source := h.destination(sourceID)
	if source == nil {
		// Terminal result is committed; A will recover via DELIVERY_STATUS.
		// The receiver must not retry an already consumed terminal ACK.
		return nil
	}
	// The durable receipt was already committed. A closed socket can race
	// the online-connection map after Chrome suspends its MV3 worker; do not
	// claim that B's accepted ACK failed merely because pushing to A failed.
	// No arbitrary client-provided reason: prevents secret leakage in ACKs.
	_ = source.send(ctx, map[string]any{"type": "SYNC_ACK", "ruleId": ruleID,
		"messageId": messageID, "status": status})
	return nil
}
