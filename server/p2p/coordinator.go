package p2p

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"relayproxy/internal/acl"
	quiccongestion "relayproxy/internal/congestion"
	"relayproxy/internal/p2p/candidate"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

const (
	DefaultLease              = 60 * time.Second
	MinLease                  = 15 * time.Second
	MaxLease                  = 5 * time.Minute
	DefaultMaxSessions        = 8
	maxActiveSessions         = 4096
	maxConnectsPerMinute      = 120
	connectRateWindow         = time.Minute
	maxFingerprintLength      = 256
	maxReportReasonLength     = 512
	maxCandidateSummaryLength = 256
	maxReportedRTTMs          = int64(600000)
	maxReportedActiveStreams  = 1000000
)

type AuthorizeFunc func(clientDeviceID, exitDeviceID string) (bool, error)

type Session struct {
	ID                uint64
	ClientDeviceID    string
	ExitDeviceID      string
	Token             []byte
	ClientCandidates  []protocol.P2PCandidate
	ExitCandidates    []protocol.P2PCandidate
	ClientFingerprint string
	ExitFingerprint   string
	BrutalUploadBPS   uint64
	BrutalDownloadBPS uint64
	ExpiresAt         time.Time
	Answered          bool
	ClientReport      PeerReport
	ExitReport        PeerReport
}

type PeerReport struct {
	Path             string
	Reason           string
	RTTMs            int64
	CandidateSummary string
	FallbackCount    uint64
	ActiveStreams    int
	BytesUp          uint64
	BytesDown        uint64
	UpdatedAt        time.Time
}

type SessionSnapshot struct {
	ID             uint64
	ClientDeviceID string
	ExitDeviceID   string
	ExpiresAt      time.Time
	Answered       bool
	ClientReport   PeerReport
	ExitReport     PeerReport
}

type connectWindow struct {
	started time.Time
	count   int
}

type Coordinator struct {
	sessions          *session.Manager
	authorize         AuthorizeFunc
	lease             time.Duration
	rendezvousAddress string
	maxPerDevice      int
	relayPolicy       *acl.Policy

	mu             sync.Mutex
	active         map[uint64]*Session
	sessionCounts  map[string]int
	connectWindows map[string]connectWindow
	cancel         context.CancelFunc
	started        bool
	send           func(*session.DeviceSession, protocol.P2PControlMessage) error
}

func NewCoordinator(sessions *session.Manager, authorize AuthorizeFunc, lease time.Duration, rendezvousAddress string, maxPerDevice int, relayPolicies ...acl.Policy) *Coordinator {
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease < MinLease {
		lease = MinLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	if maxPerDevice <= 0 {
		maxPerDevice = DefaultMaxSessions
	}
	c := &Coordinator{
		sessions: sessions, authorize: authorize, lease: lease,
		rendezvousAddress: rendezvousAddress, maxPerDevice: maxPerDevice,
		active: make(map[uint64]*Session), sessionCounts: make(map[string]int),
		connectWindows: make(map[string]connectWindow),
	}
	if len(relayPolicies) > 0 {
		policy := clonePolicy(relayPolicies[0])
		c.relayPolicy = &policy
	}
	c.send = c.notify
	return c
}

func (c *Coordinator) LeaseSeconds() int         { return int(c.lease / time.Second) }
func (c *Coordinator) RendezvousAddress() string { return c.rendezvousAddress }
func (c *Coordinator) ActiveSessions() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}

func (c *Coordinator) Snapshot(id uint64) (SessionSnapshot, bool) {
	if c == nil {
		return SessionSnapshot{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.active[id]
	if item == nil {
		return SessionSnapshot{}, false
	}
	return SessionSnapshot{
		ID: item.ID, ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
		ExpiresAt: item.ExpiresAt, Answered: item.Answered,
		ClientReport: item.ClientReport, ExitReport: item.ExitReport,
	}, true
}

func (c *Coordinator) Snapshots() []SessionSnapshot {
	if c == nil {
		return []SessionSnapshot{}
	}
	c.mu.Lock()
	out := make([]SessionSnapshot, 0, len(c.active))
	for _, item := range c.active {
		out = append(out, SessionSnapshot{
			ID: item.ID, ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
			ExpiresAt: item.ExpiresAt, Answered: item.Answered,
			ClientReport: item.ClientReport, ExitReport: item.ExitReport,
		})
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpiresAt.Equal(out[j].ExpiresAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].ExpiresAt.Before(out[j].ExpiresAt)
	})
	return out
}

func (c *Coordinator) Start(ctx context.Context) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	workCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.mu.Unlock()
	go c.sweep(workCtx)
}

func (c *Coordinator) Close() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.started = false
	items := make([]*Session, 0, len(c.active))
	for id := range c.active {
		if item := c.removeLocked(id); item != nil {
			items = append(items, item)
		}
	}
	c.connectWindows = make(map[string]connectWindow)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, item := range items {
		c.notifyPeers(item, protocol.P2PControlMessage{
			Type: protocol.P2PControlClose, SessionID: item.ID,
			ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
			Reason: "server_shutdown",
		})
	}
}

func (c *Coordinator) sweep(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			var expired []*Session
			c.mu.Lock()
			for deviceID, window := range c.connectWindows {
				if now.Sub(window.started) >= connectRateWindow {
					delete(c.connectWindows, deviceID)
				}
			}
			for id, item := range c.active {
				if !now.Before(item.ExpiresAt) {
					if removed := c.removeLocked(id); removed != nil {
						expired = append(expired, removed)
					}
				}
			}
			c.mu.Unlock()
			for _, item := range expired {
				c.notifyPeers(item, protocol.P2PControlMessage{
					Type: protocol.P2PControlClose, SessionID: item.ID,
					ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
					Reason: "lease_expired",
				})
			}
		}
	}
}

func (c *Coordinator) HandleControl(ctx context.Context, stream tunnel.TunnelStream, device *session.DeviceSession) {
	defer stream.Close()
	if device == nil {
		return
	}
	var message protocol.P2PControlMessage
	if err := protocol.ReadJSON(stream, &message); err != nil {
		log.Printf("[P2P] control decode failed device=%s: %v", device.DeviceID, err)
		return
	}
	var response protocol.P2PControlMessage
	switch message.Type {
	case protocol.P2PControlConnectRequest:
		response = c.connect(device, message)
	case protocol.P2PControlConnectAnswer:
		response = c.answer(device, message)
	case protocol.P2PControlCandidateUpdate:
		response = c.candidateUpdate(device, message)
	case protocol.P2PControlLeaseRenew:
		response = c.renew(device, message)
	case protocol.P2PControlClose:
		if !c.validPeer(device.DeviceID, message.SessionID, message.SessionToken) {
			response = p2pError("SESSION_TOKEN_INVALID", "P2P session token is invalid")
		} else {
			c.closeSession(device.DeviceID, message.SessionID, "peer_closed", false)
			response = protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID}
		}
	case protocol.P2PControlPathReport:
		response = c.pathReport(device, message)
	default:
		response = p2pError("UNKNOWN_CONTROL", "unsupported P2P control message")
	}

	if response.Type == protocol.P2PControlError {
		log.Printf("[P2P] control rejected type=%s device=%s session=%d exit=%s code=%s",
			message.Type, device.DeviceID, message.SessionID, message.ExitDeviceID, response.ErrorCode)
	} else {
		switch message.Type {
		case protocol.P2PControlConnectRequest:
			log.Printf("[P2P] session offered session=%d client=%s exit=%s client_candidates=%d",
				response.SessionID, device.DeviceID, message.ExitDeviceID, len(message.Candidates))
		case protocol.P2PControlConnectAnswer:
			log.Printf("[P2P] session answered session=%d exit=%s exit_candidates=%d",
				message.SessionID, device.DeviceID, len(message.Candidates))
		case protocol.P2PControlCandidateUpdate:
			log.Printf("[P2P] candidates updated session=%d device=%s candidates=%d",
				message.SessionID, device.DeviceID, len(message.Candidates))
		case protocol.P2PControlPathReport:
			log.Printf("[P2P] path report session=%d device=%s path=%s rtt_ms=%d fallback=%d active_streams=%d",
				message.SessionID, device.DeviceID, message.Path, message.RTTMs, message.FallbackCount, message.ActiveStreams)
		case protocol.P2PControlClose:
			log.Printf("[P2P] session closed session=%d device=%s", message.SessionID, device.DeviceID)
		}
	}
	if response.Type != "" {
		_ = protocol.WriteJSON(stream, response)
	}
}

func (c *Coordinator) connect(client *session.DeviceSession, message protocol.P2PControlMessage) protocol.P2PControlMessage {
	if !supports(client, protocol.CapabilityProxyClient) {
		return p2pError(protocol.ErrCodeAccessDenied, "device is not approved for proxy client access")
	}
	if !hasCapability(client.Capabilities, protocol.CapabilityProxyP2P) {
		return p2pError("UNSUPPORTED", "client does not advertise proxy_p2p_v1")
	}
	if message.ExitDeviceID == "" || message.ExitDeviceID == client.DeviceID {
		return p2pError(protocol.ErrCodeInvalidRequest, "exit device id is required")
	}
	fingerprint, err := normalizeFingerprint(message.CertFingerprint)
	if err != nil {
		return p2pError(protocol.ErrCodeInvalidRequest, err.Error())
	}
	validated, err := validateProxyCandidates(message.Candidates)
	if err != nil {
		return p2pError("INVALID_CANDIDATES", err.Error())
	}
	if c.sessions == nil {
		return p2pError("AUTH_UNAVAILABLE", "P2P session manager is unavailable")
	}
	exit, ok := c.sessions.Get(message.ExitDeviceID)
	if !ok || exit == nil || !supports(exit, protocol.CapabilityProxyExit) {
		return p2pError(protocol.ErrCodeExitOffline, "exit is offline")
	}
	if !hasCapability(exit.Capabilities, protocol.CapabilityProxyP2P) {
		return p2pError("UNSUPPORTED", "exit does not advertise proxy_p2p_v1")
	}
	if allowed, err := c.authorizedPair(client, exit); err != nil {
		return p2pError("AUTH_UNAVAILABLE", err.Error())
	} else if !allowed {
		return p2pError(protocol.ErrCodeAccessDenied, "client is not authorized for this exit")
	}

	id, err := randomID()
	if err != nil {
		return p2pError(protocol.ErrCodeInternalError, "failed to allocate P2P session")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return p2pError(protocol.ErrCodeInternalError, "failed to allocate P2P session token")
	}
	item := &Session{
		ID: id, ClientDeviceID: client.DeviceID, ExitDeviceID: exit.DeviceID,
		Token: append([]byte(nil), token...), ClientCandidates: append([]protocol.P2PCandidate(nil), validated...),
		ClientFingerprint: fingerprint,
		BrutalUploadBPS:   quiccongestion.CapRequestedRate(message.BrutalUploadBPS, 0),
		BrutalDownloadBPS: quiccongestion.CapRequestedRate(message.BrutalDownloadBPS, 0),
		ExpiresAt:         time.Now().Add(c.lease),
	}
	now := time.Now()
	c.mu.Lock()
	if !c.allowConnectLocked(client.DeviceID, now) {
		c.mu.Unlock()
		return p2pError(protocol.ErrCodeRateLimited, "too many P2P session requests")
	}
	if len(c.active) >= maxActiveSessions || c.sessionCounts[client.DeviceID] >= c.maxPerDevice || c.sessionCounts[exit.DeviceID] >= c.maxPerDevice {
		c.mu.Unlock()
		return p2pError(protocol.ErrCodeConnectionLimit, "P2P session capacity reached")
	}
	if _, exists := c.active[id]; exists {
		c.mu.Unlock()
		return p2pError(protocol.ErrCodeInternalError, "P2P session id collision")
	}
	c.active[id] = item
	c.sessionCounts[client.DeviceID]++
	c.sessionCounts[exit.DeviceID]++
	c.mu.Unlock()

	offer := protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectOffer, SessionID: id,
		ClientDeviceID: client.DeviceID, ExitDeviceID: exit.DeviceID,
		SessionToken: append([]byte(nil), token...), Candidates: append([]protocol.P2PCandidate(nil), validated...),
		CertFingerprint: fingerprint, PeerFingerprint: fingerprint,
		PeerCapabilities: peerP2PCapabilities(client), RelayPolicy: c.policyCopy(),
		BrutalUploadBPS: item.BrutalUploadBPS, BrutalDownloadBPS: item.BrutalDownloadBPS,
		LeaseExpiresAt: item.ExpiresAt.UnixMilli(), RendezvousAddress: c.rendezvousAddress, LeaseSec: c.LeaseSeconds(),
	}
	if err := c.send(exit, offer); err != nil {
		c.mu.Lock()
		c.removeLocked(id)
		c.mu.Unlock()
		return p2pError("EXIT_NOTIFY_FAILED", "failed to deliver P2P offer to exit")
	}
	return protocol.P2PControlMessage{
		Type: protocol.P2PControlLeaseAck, SessionID: id,
		ClientDeviceID: client.DeviceID, ExitDeviceID: exit.DeviceID,
		SessionToken: append([]byte(nil), token...), LeaseExpiresAt: item.ExpiresAt.UnixMilli(),
		BrutalUploadBPS: item.BrutalUploadBPS, BrutalDownloadBPS: item.BrutalDownloadBPS,
		PeerCapabilities:  peerP2PCapabilities(exit),
		RendezvousAddress: c.rendezvousAddress, LeaseSec: c.LeaseSeconds(),
	}
}

func (c *Coordinator) answer(exit *session.DeviceSession, message protocol.P2PControlMessage) protocol.P2PControlMessage {
	if !supports(exit, protocol.CapabilityProxyExit) || !hasCapability(exit.Capabilities, protocol.CapabilityProxyP2P) {
		return p2pError(protocol.ErrCodeAccessDenied, "device is not an eligible P2P exit")
	}
	fingerprint, err := normalizeFingerprint(message.CertFingerprint)
	if err != nil {
		return p2pError(protocol.ErrCodeInvalidRequest, err.Error())
	}
	validated, err := validateProxyCandidates(message.Candidates)
	if err != nil {
		return p2pError("INVALID_CANDIDATES", err.Error())
	}
	item, client, ok, authErr := c.authorizedSessionPeer(exit.DeviceID, message.SessionID, message.SessionToken)
	if authErr != nil {
		return p2pError("AUTH_UNAVAILABLE", authErr.Error())
	}
	if !ok || item == nil || item.ExitDeviceID != exit.DeviceID {
		return p2pError("SESSION_TOKEN_INVALID", "P2P session is no longer active or token is invalid")
	}
	if client == nil {
		return p2pError("PEER_OFFLINE", "P2P client is offline")
	}

	c.mu.Lock()
	current := c.active[message.SessionID]
	if current == nil || current != item || !bytes.Equal(current.Token, message.SessionToken) {
		c.mu.Unlock()
		return p2pError("SESSION_NOT_FOUND", "P2P session is no longer active")
	}
	current.ExitCandidates = append([]protocol.P2PCandidate(nil), validated...)
	current.ExitFingerprint = fingerprint
	current.Answered = true
	expires := current.ExpiresAt.UnixMilli()
	c.mu.Unlock()

	answer := protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectAnswer, SessionID: item.ID,
		ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
		SessionToken: append([]byte(nil), item.Token...), Candidates: append([]protocol.P2PCandidate(nil), validated...),
		CertFingerprint: fingerprint, PeerFingerprint: fingerprint,
		PeerCapabilities: peerP2PCapabilities(exit),
		BrutalUploadBPS:  item.BrutalUploadBPS, BrutalDownloadBPS: item.BrutalDownloadBPS,
		LeaseExpiresAt: expires, RendezvousAddress: c.rendezvousAddress, LeaseSec: c.LeaseSeconds(),
	}
	if err := c.send(client, answer); err != nil {
		return p2pError("CLIENT_NOTIFY_FAILED", "failed to deliver P2P answer to client")
	}
	return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: item.ID, LeaseExpiresAt: expires}
}

func (c *Coordinator) candidateUpdate(device *session.DeviceSession, message protocol.P2PControlMessage) protocol.P2PControlMessage {
	validated, err := validateProxyCandidates(message.Candidates)
	if err != nil {
		return p2pError("INVALID_CANDIDATES", err.Error())
	}
	item, other, ok, authErr := c.authorizedSessionPeer(device.DeviceID, message.SessionID, message.SessionToken)
	if authErr != nil {
		return p2pError("AUTH_UNAVAILABLE", authErr.Error())
	}
	if !ok || item == nil {
		return p2pError("SESSION_TOKEN_INVALID", "P2P session is no longer active or token is invalid")
	}
	c.mu.Lock()
	current := c.active[message.SessionID]
	if current == nil || current != item || !bytes.Equal(current.Token, message.SessionToken) {
		c.mu.Unlock()
		return p2pError("SESSION_NOT_FOUND", "P2P session is no longer active")
	}
	if device.DeviceID == current.ClientDeviceID {
		current.ClientCandidates = append([]protocol.P2PCandidate(nil), validated...)
	} else {
		current.ExitCandidates = append([]protocol.P2PCandidate(nil), validated...)
	}
	forward := protocol.P2PControlMessage{
		Type: protocol.P2PControlCandidateUpdate, SessionID: current.ID,
		ClientDeviceID: current.ClientDeviceID, ExitDeviceID: current.ExitDeviceID,
		SessionToken: append([]byte(nil), current.Token...), Candidates: append([]protocol.P2PCandidate(nil), validated...),
		LeaseExpiresAt: current.ExpiresAt.UnixMilli(),
	}
	c.mu.Unlock()
	if other == nil {
		return p2pError("PEER_OFFLINE", "P2P peer is offline")
	}
	if err := c.send(other, forward); err != nil {
		return p2pError("PEER_NOTIFY_FAILED", "failed to forward P2P candidates")
	}
	return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID, LeaseExpiresAt: forward.LeaseExpiresAt}
}

func (c *Coordinator) renew(device *session.DeviceSession, message protocol.P2PControlMessage) protocol.P2PControlMessage {
	item, _, ok, authErr := c.authorizedSessionPeer(device.DeviceID, message.SessionID, message.SessionToken)
	if authErr != nil {
		return p2pError("AUTH_UNAVAILABLE", authErr.Error())
	}
	if !ok || item == nil {
		return p2pError("SESSION_TOKEN_INVALID", "P2P session is no longer active or token is invalid")
	}
	c.mu.Lock()
	current := c.active[message.SessionID]
	if current == nil || current != item || !bytes.Equal(current.Token, message.SessionToken) {
		c.mu.Unlock()
		return p2pError("SESSION_NOT_FOUND", "P2P session is no longer active")
	}
	current.ExpiresAt = time.Now().Add(c.lease)
	expires := current.ExpiresAt.UnixMilli()
	c.mu.Unlock()
	return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: item.ID, LeaseExpiresAt: expires}
}

func validCandidateSummary(summary string) bool {
	if summary == "" {
		return true
	}
	if len(summary) > maxCandidateSummaryLength || strings.ContainsAny(summary, "\r\n\t") {
		return false
	}
	var localLAN, localReflexive, peerLAN, peerReflexive int
	n, err := fmt.Sscanf(summary, "local:lan=%d,reflexive=%d;peer:lan=%d,reflexive=%d",
		&localLAN, &localReflexive, &peerLAN, &peerReflexive)
	if err != nil || n != 4 {
		return false
	}
	for _, count := range []int{localLAN, localReflexive, peerLAN, peerReflexive} {
		if count < 0 || count > 16 {
			return false
		}
	}
	return summary == fmt.Sprintf("local:lan=%d,reflexive=%d;peer:lan=%d,reflexive=%d",
		localLAN, localReflexive, peerLAN, peerReflexive)
}

func (c *Coordinator) pathReport(device *session.DeviceSession, message protocol.P2PControlMessage) protocol.P2PControlMessage {
	if device == nil {
		return p2pError("SESSION_TOKEN_INVALID", "P2P session token is invalid")
	}
	if message.Path != "" && message.Path != protocol.P2PPathDirectQUIC && message.Path != protocol.P2PPathRelayQUIC && message.Path != protocol.P2PPathRelayTLS {
		return p2pError(protocol.ErrCodeInvalidRequest, "unsupported P2P path")
	}
	if message.ActiveStreams < 0 || message.ActiveStreams > maxReportedActiveStreams {
		return p2pError(protocol.ErrCodeInvalidRequest, "invalid P2P active stream count")
	}
	if message.RTTMs < 0 || message.RTTMs > maxReportedRTTMs {
		return p2pError(protocol.ErrCodeInvalidRequest, "invalid P2P RTT")
	}
	message.CandidateSummary = strings.TrimSpace(message.CandidateSummary)
	if !validCandidateSummary(message.CandidateSummary) {
		return p2pError(protocol.ErrCodeInvalidRequest, "invalid P2P candidate summary")
	}
	reason := strings.TrimSpace(message.Reason)
	if len(reason) > maxReportReasonLength || strings.ContainsAny(reason, "\r\n\t") {
		return p2pError(protocol.ErrCodeInvalidRequest, "invalid P2P path report reason")
	}

	now := time.Now()
	c.mu.Lock()
	item := c.active[message.SessionID]
	if item == nil || !now.Before(item.ExpiresAt) ||
		(device.DeviceID != item.ClientDeviceID && device.DeviceID != item.ExitDeviceID) ||
		!bytes.Equal(message.SessionToken, item.Token) {
		if item != nil && !now.Before(item.ExpiresAt) {
			c.removeLocked(message.SessionID)
		}
		c.mu.Unlock()
		return p2pError("SESSION_TOKEN_INVALID", "P2P session token is invalid")
	}
	report := PeerReport{
		Path: message.Path, Reason: reason, RTTMs: message.RTTMs,
		CandidateSummary: message.CandidateSummary, FallbackCount: message.FallbackCount,
		ActiveStreams: message.ActiveStreams, BytesUp: message.BytesUp, BytesDown: message.BytesDown, UpdatedAt: now,
	}
	if device.DeviceID == item.ClientDeviceID {
		item.ClientReport = report
	} else {
		item.ExitReport = report
	}
	c.mu.Unlock()
	return protocol.P2PControlMessage{Type: protocol.P2PControlLeaseAck, SessionID: message.SessionID}
}

func (c *Coordinator) authorizedSessionPeer(deviceID string, id uint64, token []byte) (*Session, *session.DeviceSession, bool, error) {
	c.mu.Lock()
	item := c.active[id]
	if item == nil || !time.Now().Before(item.ExpiresAt) ||
		(deviceID != item.ClientDeviceID && deviceID != item.ExitDeviceID) || !bytes.Equal(token, item.Token) {
		if item != nil && !time.Now().Before(item.ExpiresAt) {
			c.removeLocked(id)
		}
		c.mu.Unlock()
		return nil, nil, false, nil
	}
	clientID, exitID := item.ClientDeviceID, item.ExitDeviceID
	c.mu.Unlock()

	if c.sessions == nil {
		return nil, nil, false, errors.New("P2P session manager is unavailable")
	}
	client, clientOK := c.sessions.Get(clientID)
	exit, exitOK := c.sessions.Get(exitID)
	if !clientOK || !exitOK || client == nil || exit == nil ||
		!supports(client, protocol.CapabilityProxyClient) || !supports(exit, protocol.CapabilityProxyExit) ||
		!hasCapability(client.Capabilities, protocol.CapabilityProxyP2P) || !hasCapability(exit.Capabilities, protocol.CapabilityProxyP2P) {
		return item, nil, false, nil
	}
	allowed, err := c.authorizedPair(client, exit)
	if err != nil || !allowed {
		if err == nil {
			c.closeSession("", id, "authorization_revoked", true)
		}
		return item, nil, false, err
	}
	other := exit
	if deviceID == exitID {
		other = client
	}
	return item, other, true, nil
}

func (c *Coordinator) authorizedPair(client, exit *session.DeviceSession) (bool, error) {
	if client == nil || exit == nil || client.DeviceID == exit.DeviceID {
		return false, nil
	}
	if client.IdentityID != "" || exit.IdentityID != "" {
		if client.IdentityID == "" || exit.IdentityID == "" {
			return false, nil
		}
		if c.authorize != nil {
			return c.authorize(client.DeviceID, exit.DeviceID)
		}
		return false, nil
	}
	if client.OwnerUserID != "" && exit.OwnerUserID != "" && client.OwnerUserID != exit.OwnerUserID {
		return false, nil
	}
	if c.authorize != nil {
		return c.authorize(client.DeviceID, exit.DeviceID)
	}
	return client.OwnerUserID != "" && client.OwnerUserID == exit.OwnerUserID, nil
}

func (c *Coordinator) validPeer(deviceID string, id uint64, token []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	item := c.active[id]
	return item != nil && time.Now().Before(item.ExpiresAt) &&
		(deviceID == item.ClientDeviceID || deviceID == item.ExitDeviceID) && bytes.Equal(token, item.Token)
}

func (c *Coordinator) closeSession(deviceID string, id uint64, reason string, revoke bool) {
	c.mu.Lock()
	item := c.active[id]
	if item == nil || (deviceID != "" && deviceID != item.ClientDeviceID && deviceID != item.ExitDeviceID) {
		c.mu.Unlock()
		return
	}
	item = c.removeLocked(id)
	c.mu.Unlock()
	if item == nil {
		return
	}
	kind := protocol.P2PControlClose
	if revoke {
		kind = protocol.P2PControlRevoke
	}
	c.notifyPeers(item, protocol.P2PControlMessage{
		Type: kind, SessionID: item.ID, ClientDeviceID: item.ClientDeviceID,
		ExitDeviceID: item.ExitDeviceID, Reason: reason,
	})
}

func (c *Coordinator) CloseDevice(deviceID string) {
	c.closeDevice(deviceID, "device_revoked_or_offline")
}

func (c *Coordinator) RevokeDevice(deviceID string) {
	c.closeDevice(deviceID, "device_revoked")
}

func (c *Coordinator) closeDevice(deviceID, reason string) {
	c.mu.Lock()
	var items []*Session
	for id, item := range c.active {
		if item.ClientDeviceID == deviceID || item.ExitDeviceID == deviceID {
			if removed := c.removeLocked(id); removed != nil {
				items = append(items, removed)
			}
		}
	}
	delete(c.connectWindows, deviceID)
	c.mu.Unlock()
	for _, item := range items {
		c.notifyPeers(item, protocol.P2PControlMessage{
			Type: protocol.P2PControlRevoke, SessionID: item.ID,
			ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID, Reason: reason,
		})
	}
}

func (c *Coordinator) notify(device *session.DeviceSession, message protocol.P2PControlMessage) error {
	if device == nil || device.Tunnel == nil {
		return errors.New("P2P peer is offline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := device.Tunnel.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeP2PControl,
		RequestID: fmt.Sprintf("p2p-%d", message.SessionID), ExitDeviceID: device.DeviceID,
	}); err != nil {
		return err
	}
	return protocol.WriteJSON(stream, message)
}

func (c *Coordinator) notifyPeers(item *Session, message protocol.P2PControlMessage) {
	if item == nil || c.sessions == nil {
		return
	}
	if client, ok := c.sessions.Get(item.ClientDeviceID); ok {
		_ = c.send(client, message)
	}
	if exit, ok := c.sessions.Get(item.ExitDeviceID); ok {
		_ = c.send(exit, message)
	}
}

func (c *Coordinator) allowConnectLocked(deviceID string, now time.Time) bool {
	window := c.connectWindows[deviceID]
	if window.started.IsZero() || now.Sub(window.started) >= connectRateWindow {
		c.connectWindows[deviceID] = connectWindow{started: now, count: 1}
		return true
	}
	if window.count >= maxConnectsPerMinute {
		return false
	}
	window.count++
	c.connectWindows[deviceID] = window
	return true
}

func (c *Coordinator) removeLocked(id uint64) *Session {
	item := c.active[id]
	if item == nil {
		return nil
	}
	delete(c.active, id)
	for _, deviceID := range []string{item.ClientDeviceID, item.ExitDeviceID} {
		if count := c.sessionCounts[deviceID]; count <= 1 {
			delete(c.sessionCounts, deviceID)
		} else {
			c.sessionCounts[deviceID] = count - 1
		}
	}
	return item
}

func randomID() (uint64, error) {
	var id uint64
	if err := binary.Read(rand.Reader, binary.BigEndian, &id); err != nil {
		return 0, err
	}
	if id == 0 {
		id = uint64(time.Now().UnixNano())
	}
	return id, nil
}

func normalizeFingerprint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("ephemeral certificate fingerprint is required")
	}
	if len(value) > maxFingerprintLength || strings.ContainsAny(value, "\r\n\t") {
		return "", errors.New("invalid ephemeral certificate fingerprint")
	}
	return value, nil
}

func validateProxyCandidates(input []protocol.P2PCandidate) ([]protocol.P2PCandidate, error) {
	validated, err := candidate.Validate(input)
	if err != nil {
		return nil, err
	}
	if len(validated) == 0 {
		return nil, errors.New("at least one UDP candidate is required")
	}
	for _, item := range validated {
		if item.Protocol != "udp" {
			return nil, errors.New("proxy P2P v1 only supports UDP candidates")
		}
	}
	return validated, nil
}

func supports(device *session.DeviceSession, grant string) bool {
	return device != nil && hasCapability(device.Grants, grant)
}

func hasCapability(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// peerP2PCapabilities publishes only capabilities that affect the direct
// Client <-> Exit data path. Values are derived from the authenticated device
// session, never from peer-provided P2P control payloads.
func peerP2PCapabilities(device *session.DeviceSession) []string {
	if device == nil {
		return nil
	}
	caps := make([]string, 0, 1)
	if hasCapability(device.Capabilities, protocol.CapabilityProxyStreamResume) {
		caps = append(caps, protocol.CapabilityProxyStreamResume)
	}
	return caps
}

func p2pError(code, message string) protocol.P2PControlMessage {
	return protocol.P2PControlMessage{Type: protocol.P2PControlError, ErrorCode: code, ErrorMessage: message}
}

func (c *Coordinator) policyCopy() *acl.Policy {
	if c == nil || c.relayPolicy == nil {
		return nil
	}
	policy := clonePolicy(*c.relayPolicy)
	return &policy
}

func clonePolicy(policy acl.Policy) acl.Policy {
	policy.Rules = slices.Clone(policy.Rules)
	policy.AccessHosts = slices.Clone(policy.AccessHosts)
	policy.AccessCIDRs = slices.Clone(policy.AccessCIDRs)
	return policy
}
