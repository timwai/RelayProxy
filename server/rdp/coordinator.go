// Package rdp contains the server-owned RDP rendezvous and ingress services.
// It never accepts a host/port from a controller: target identity and the
// local RDP service are resolved from the approved database grant.
package rdp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/rdp/candidate"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

const (
	DefaultLease = 60 * time.Second
	MinLease     = 15 * time.Second
	MaxLease     = 5 * time.Minute

	// Direct RDP sessions own sockets and goroutines on both endpoints. Keep
	// the coordinator bounded even when an already-authorized controller is
	// buggy or compromised.
	maxActiveLeases      = 1024
	maxLeasesPerDevice   = 64
	maxConnectsPerMinute = 120
	connectRateWindow    = time.Minute
)

type Registration struct {
	DeviceID   string
	Candidates []protocol.RDPCandidate
	UpdatedAt  time.Time
}

type Lease struct {
	ID                   uint64
	Purpose              string
	ControllerID         string
	TargetID             string
	Token                []byte
	ControllerCandidates []protocol.RDPCandidate
	TargetCandidates     []protocol.RDPCandidate
	ExpiresAt            time.Time
}

type connectWindow struct {
	started time.Time
	count   int
}

type Coordinator struct {
	sessions          *session.Manager
	db                *repository.DB
	lease             time.Duration
	rendezvousAddress string
	mu                sync.Mutex
	registrations     map[string]Registration
	leases            map[uint64]*Lease
	leaseCounts       map[string]int
	connectWindows    map[string]connectWindow
	cancel            context.CancelFunc
	started           bool
}

func NewCoordinator(sessions *session.Manager, db *repository.DB, lease time.Duration, rendezvousAddress string) *Coordinator {
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease < MinLease {
		lease = MinLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	return &Coordinator{sessions: sessions, db: db, lease: lease, rendezvousAddress: rendezvousAddress,
		registrations: make(map[string]Registration), leases: make(map[uint64]*Lease),
		leaseCounts: make(map[string]int), connectWindows: make(map[string]connectWindow)}
}

func (c *Coordinator) LeaseSeconds() int         { return int(c.lease / time.Second) }
func (c *Coordinator) RendezvousAddress() string { return c.rendezvousAddress }

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
	leases := make([]*Lease, 0, len(c.leases))
	for _, lease := range c.leases {
		leases = append(leases, lease)
	}
	c.leases = make(map[uint64]*Lease)
	c.leaseCounts = make(map[string]int)
	c.registrations = make(map[string]Registration)
	c.connectWindows = make(map[string]connectWindow)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, lease := range leases {
		c.notifyLease(lease, protocol.RDPControlMessage{Type: protocol.RDPControlSessionClose, Purpose: lease.Purpose, SessionID: lease.ID, ControllerID: lease.ControllerID, TargetID: lease.TargetID, ErrorCode: "SERVER_SHUTDOWN"})
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
			var expired []*Lease
			c.mu.Lock()
			for deviceID, window := range c.connectWindows {
				if now.Sub(window.started) >= connectRateWindow {
					delete(c.connectWindows, deviceID)
				}
			}
			for id, lease := range c.leases {
				if !now.Before(lease.ExpiresAt) {
					if removed := c.removeLeaseLocked(id); removed != nil {
						expired = append(expired, removed)
					}
				}
			}
			c.mu.Unlock()
			for _, lease := range expired {
				c.notifyLease(lease, protocol.RDPControlMessage{Type: protocol.RDPControlSessionClose, Purpose: lease.Purpose, SessionID: lease.ID, ControllerID: lease.ControllerID, TargetID: lease.TargetID, ErrorCode: "LEASE_EXPIRED"})
			}
		}
	}
}

// HandleControl is called by StreamRouter for one short-lived control stream.
// A caller may send one request and receives at most one response.
func (c *Coordinator) HandleControl(ctx context.Context, stream tunnel.TunnelStream, device *session.DeviceSession) {
	defer stream.Close()
	if device == nil {
		return
	}
	var message protocol.RDPControlMessage
	if err := protocol.ReadJSON(stream, &message); err != nil {
		return
	}
	message.ControllerID = device.DeviceID
	var response protocol.RDPControlMessage
	switch message.Type {
	case protocol.RDPControlRegister:
		response = c.register(device.DeviceID, message.Candidates)
	case protocol.RDPControlConnectRequest:
		response = c.connect(device.DeviceID, message)
	case protocol.RDPControlCandidateUpdate:
		response = c.candidateUpdate(device.DeviceID, message)
	case protocol.RDPControlLeaseRenew:
		response = c.renew(device.DeviceID, message)
	case protocol.RDPControlSessionClose:
		if !c.validLeasePeer(device.DeviceID, message.SessionID, message.SessionToken) {
			response = rdpError("SESSION_TOKEN_INVALID", "RDP session token is invalid")
		} else {
			c.closeLease(device.DeviceID, message.SessionID, "PEER_CLOSED")
			response = protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck, SessionID: message.SessionID}
		}
	default:
		response = protocol.RDPControlMessage{Type: protocol.RDPControlError, ErrorCode: "UNKNOWN_CONTROL", ErrorMessage: "unsupported RDP control message"}
	}
	if response.Type != "" {
		_ = protocol.WriteJSON(stream, response)
	}
}

func (c *Coordinator) register(deviceID string, raw []protocol.RDPCandidate) protocol.RDPControlMessage {
	validated, err := candidate.Validate(raw)
	if err != nil {
		return protocol.RDPControlMessage{Type: protocol.RDPControlError, ErrorCode: "INVALID_CANDIDATES", ErrorMessage: err.Error()}
	}
	c.mu.Lock()
	if c.registrations == nil {
		c.registrations = make(map[string]Registration)
	}
	c.registrations[deviceID] = Registration{DeviceID: deviceID, Candidates: validated, UpdatedAt: time.Now()}
	c.mu.Unlock()
	return protocol.RDPControlMessage{Type: protocol.RDPControlRegisterAck, RendezvousAddress: c.rendezvousAddress, LeaseSec: c.LeaseSeconds(), RDPOnline: true}
}

func normalizeP2PPurpose(value string) string {
	switch value {
	case "", protocol.P2PPurposeRDP:
		return protocol.P2PPurposeRDP
	case protocol.P2PPurposeDesktopMedia:
		return protocol.P2PPurposeDesktopMedia
	default:
		return ""
	}
}

func (c *Coordinator) authorizePurpose(controllerID, targetID, purpose string) (bool, error) {
	if c.db == nil {
		return false, errors.New("P2P authorization is unavailable")
	}
	switch normalizeP2PPurpose(purpose) {
	case protocol.P2PPurposeRDP:
		return c.db.AuthorizeRDP(controllerID, targetID)
	case protocol.P2PPurposeDesktopMedia:
		return c.db.AuthorizeDesktop(controllerID, targetID)
	default:
		return false, nil
	}
}

func purposeTargetCapability(purpose string) string {
	if normalizeP2PPurpose(purpose) == protocol.P2PPurposeDesktopMedia {
		return protocol.CapabilityDesktopHost
	}
	return protocol.CapabilityRDPHost
}

func (c *Coordinator) connect(controllerID string, message protocol.RDPControlMessage) protocol.RDPControlMessage {
	purpose := normalizeP2PPurpose(message.Purpose)
	if purpose == "" {
		return rdpError(protocol.ErrCodeInvalidRequest, "unsupported P2P session purpose")
	}
	if controllerID == "" || message.TargetID == "" || controllerID == message.TargetID {
		return rdpError("INVALID_TARGET", "P2P target is required")
	}
	now := time.Now()
	c.mu.Lock()
	if c.connectWindows == nil {
		c.connectWindows = make(map[string]connectWindow)
	}
	if !c.allowConnectLocked(controllerID, now) {
		c.mu.Unlock()
		return rdpError(protocol.ErrCodeRateLimited, "too many P2P session requests")
	}
	c.mu.Unlock()

	ok, err := c.authorizePurpose(controllerID, message.TargetID, purpose)
	if err != nil {
		return rdpError("AUTH_UNAVAILABLE", err.Error())
	}
	if !ok {
		return rdpError(protocol.ErrCodeAccessDenied, "controller is not authorized for this P2P target")
	}
	targetSession, online := c.sessions.Get(message.TargetID)
	if !online || targetSession == nil || !contains(targetSession.Grants, purposeTargetCapability(purpose)) {
		return rdpError("TARGET_OFFLINE", "P2P target is offline")
	}

	controllerCandidates := c.registration(controllerID)
	targetCandidates := c.registration(message.TargetID)
	id, err := randomID()
	if err != nil {
		return rdpError("INTERNAL", "failed to allocate P2P session")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return rdpError("INTERNAL", "failed to allocate P2P session token")
	}
	lease := &Lease{
		ID: id, Purpose: purpose, ControllerID: controllerID, TargetID: message.TargetID, Token: token,
		ControllerCandidates: controllerCandidates, TargetCandidates: targetCandidates, ExpiresAt: time.Now().Add(c.lease),
	}
	c.mu.Lock()
	if c.leases == nil {
		c.leases = make(map[uint64]*Lease)
	}
	if c.leaseCounts == nil {
		c.leaseCounts = make(map[string]int)
	}
	if len(c.leases) >= maxActiveLeases || c.leaseCounts[controllerID] >= maxLeasesPerDevice || c.leaseCounts[message.TargetID] >= maxLeasesPerDevice {
		c.mu.Unlock()
		return rdpError(protocol.ErrCodeConnectionLimit, "P2P session capacity reached")
	}
	c.leases[id] = lease
	c.leaseCounts[controllerID]++
	c.leaseCounts[message.TargetID]++
	c.mu.Unlock()

	notify := protocol.RDPControlMessage{
		Type: protocol.RDPControlConnectNotify, Purpose: purpose, SessionID: id,
		ControllerID: controllerID, TargetID: message.TargetID, SessionToken: append([]byte(nil), token...),
		Candidates:     append([]protocol.RDPCandidate(nil), controllerCandidates...),
		LeaseExpiresAt: lease.ExpiresAt.UnixMilli(), RDPOnline: purpose == protocol.P2PPurposeRDP,
	}
	if err := c.notify(targetSession, notify); err != nil {
		c.mu.Lock()
		c.removeLeaseLocked(id)
		c.mu.Unlock()
		return rdpError("TARGET_NOTIFY_FAILED", "failed to notify P2P target")
	}
	return protocol.RDPControlMessage{
		Type: protocol.RDPControlConnectResponse, Purpose: purpose, SessionID: id,
		ControllerID: controllerID, TargetID: message.TargetID, SessionToken: append([]byte(nil), token...),
		Candidates:     append([]protocol.RDPCandidate(nil), targetCandidates...),
		LeaseExpiresAt: lease.ExpiresAt.UnixMilli(), RDPOnline: purpose == protocol.P2PPurposeRDP,
	}
}

func (c *Coordinator) candidateUpdate(deviceID string, message protocol.RDPControlMessage) protocol.RDPControlMessage {
	validated, err := candidate.Validate(message.Candidates)
	if err != nil {
		return rdpError("INVALID_CANDIDATES", err.Error())
	}
	c.mu.Lock()
	lease, ok := c.leases[message.SessionID]
	if ok && !time.Now().Before(lease.ExpiresAt) {
		c.removeLeaseLocked(message.SessionID)
		ok = false
	}
	if !ok || (deviceID != lease.ControllerID && deviceID != lease.TargetID) || !bytes.Equal(message.SessionToken, lease.Token) {
		c.mu.Unlock()
		return rdpError("SESSION_NOT_FOUND", "RDP session is no longer active")
	}
	controllerID, targetID := lease.ControllerID, lease.TargetID
	leaseID := lease.ID
	c.mu.Unlock()

	// Candidate exchange is a second authorization boundary.  A controller
	// grant may be revoked after connect, so do not forward fresh endpoint
	// information until the current database grant has been re-checked.
	if c.db == nil {
		return rdpError("AUTH_UNAVAILABLE", "RDP authorization is unavailable")
	}
	allowed, err := c.authorizePurpose(controllerID, targetID, lease.Purpose)
	if err != nil {
		return rdpError("AUTH_UNAVAILABLE", err.Error())
	}
	if !allowed {
		c.closeLease(deviceID, leaseID, "AUTH_REVOKED")
		return rdpError(protocol.ErrCodeAccessDenied, "RDP authorization was revoked")
	}

	// Re-check the in-memory lease after the database call so a concurrent
	// close/revoke cannot be resurrected by this update.
	c.mu.Lock()
	lease, ok = c.leases[message.SessionID]
	if ok && !time.Now().Before(lease.ExpiresAt) {
		c.removeLeaseLocked(message.SessionID)
		ok = false
	}
	if !ok || lease.ID != leaseID || lease.ControllerID != controllerID || lease.TargetID != targetID ||
		(deviceID != lease.ControllerID && deviceID != lease.TargetID) || !bytes.Equal(message.SessionToken, lease.Token) {
		c.mu.Unlock()
		return rdpError("SESSION_NOT_FOUND", "RDP session is no longer active")
	}
	if deviceID == lease.ControllerID {
		lease.ControllerCandidates = validated
	} else {
		lease.TargetCandidates = validated
	}
	otherID := lease.TargetID
	if deviceID == lease.TargetID {
		otherID = lease.ControllerID
	}
	forward := protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate, Purpose: lease.Purpose, SessionID: lease.ID, ControllerID: lease.ControllerID, TargetID: lease.TargetID, SessionToken: append([]byte(nil), lease.Token...), Candidates: append([]protocol.RDPCandidate(nil), validated...), LeaseExpiresAt: lease.ExpiresAt.UnixMilli()}
	other, otherOK := c.sessions.Get(otherID)
	c.mu.Unlock()
	if !otherOK || other == nil {
		return rdpError("PEER_OFFLINE", "RDP peer is offline")
	}
	if err := c.notify(other, forward); err != nil {
		return rdpError("PEER_NOTIFY_FAILED", "failed to forward RDP candidates")
	}
	return protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate, Purpose: lease.Purpose, SessionID: message.SessionID, LeaseExpiresAt: forward.LeaseExpiresAt}
}

func (c *Coordinator) renew(deviceID string, message protocol.RDPControlMessage) protocol.RDPControlMessage {
	c.mu.Lock()
	lease, ok := c.leases[message.SessionID]
	if ok && (deviceID != lease.ControllerID && deviceID != lease.TargetID) {
		ok = false
	}
	if ok && !bytes.Equal(message.SessionToken, lease.Token) {
		ok = false
	}
	c.mu.Unlock()
	if !ok {
		return rdpError("SESSION_TOKEN_INVALID", "RDP session is no longer active or token is invalid")
	}
	if c.db != nil {
		allowed, err := c.authorizePurpose(lease.ControllerID, lease.TargetID, lease.Purpose)
		if err != nil {
			return rdpError("AUTH_UNAVAILABLE", err.Error())
		}
		if !allowed {
			c.closeLease(deviceID, lease.ID, "AUTH_REVOKED")
			return rdpError(protocol.ErrCodeAccessDenied, "RDP authorization was revoked")
		}
	}
	c.mu.Lock()
	lease, ok = c.leases[message.SessionID]
	if ok && ((deviceID != lease.ControllerID && deviceID != lease.TargetID) || !bytes.Equal(message.SessionToken, lease.Token)) {
		ok = false
	}
	if ok {
		lease.ExpiresAt = time.Now().Add(c.lease)
	}
	expires := int64(0)
	if ok {
		expires = lease.ExpiresAt.UnixMilli()
	}
	c.mu.Unlock()
	if !ok {
		return rdpError("SESSION_NOT_FOUND", "RDP session is no longer active")
	}
	return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck, Purpose: lease.Purpose, SessionID: lease.ID, LeaseExpiresAt: expires}
}

func (c *Coordinator) validLeasePeer(deviceID string, id uint64, token []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[id]
	return ok && (deviceID == lease.ControllerID || deviceID == lease.TargetID) && bytes.Equal(token, lease.Token)
}

func (c *Coordinator) registration(id string) []protocol.RDPCandidate {
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.registrations[id]
	if !ok || time.Since(item.UpdatedAt) > 2*time.Minute {
		return nil
	}
	return append([]protocol.RDPCandidate(nil), item.Candidates...)
}

func (c *Coordinator) notify(device *session.DeviceSession, message protocol.RDPControlMessage) error {
	if device == nil || device.Tunnel == nil {
		return errors.New("RDP peer is offline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := device.Tunnel.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeRDPControl, RequestID: fmt.Sprintf("rdp-%d", message.SessionID), ExitDeviceID: device.DeviceID}); err != nil {
		return err
	}
	return protocol.WriteJSON(stream, message)
}

func (c *Coordinator) notifyLease(lease *Lease, message protocol.RDPControlMessage) {
	if lease == nil {
		return
	}
	if controller, ok := c.sessions.Get(lease.ControllerID); ok {
		_ = c.notify(controller, message)
	}
	if target, ok := c.sessions.Get(lease.TargetID); ok {
		_ = c.notify(target, message)
	}
}

func (c *Coordinator) closeLease(deviceID string, id uint64, reason string) {
	c.mu.Lock()
	lease, ok := c.leases[id]
	if !ok || (deviceID != "" && deviceID != lease.ControllerID && deviceID != lease.TargetID) {
		c.mu.Unlock()
		return
	}
	lease = c.removeLeaseLocked(id)
	c.mu.Unlock()
	if lease == nil {
		return
	}
	c.notifyLease(lease, protocol.RDPControlMessage{Type: protocol.RDPControlSessionClose, Purpose: lease.Purpose, SessionID: lease.ID, ControllerID: lease.ControllerID, TargetID: lease.TargetID, SessionToken: append([]byte(nil), lease.Token...), ErrorCode: reason})
}

// CloseDevice invalidates every in-memory lease involving a revoked or
// disconnected device.  Relay streams are closed by session.Manager; this
// function handles direct paths, which otherwise can live until lease expiry.
func (c *Coordinator) CloseDevice(deviceID string) {
	c.mu.Lock()
	var leases []*Lease
	for id, lease := range c.leases {
		if lease.ControllerID == deviceID || lease.TargetID == deviceID {
			if removed := c.removeLeaseLocked(id); removed != nil {
				leases = append(leases, removed)
			}
		}
	}
	delete(c.registrations, deviceID)
	delete(c.connectWindows, deviceID)
	c.mu.Unlock()
	for _, lease := range leases {
		c.notifyLease(lease, protocol.RDPControlMessage{Type: protocol.RDPControlSessionClose, Purpose: lease.Purpose, SessionID: lease.ID, ControllerID: lease.ControllerID, TargetID: lease.TargetID, SessionToken: append([]byte(nil), lease.Token...), ErrorCode: "DEVICE_REVOKED_OR_OFFLINE"})
	}
}

// allowConnectLocked applies a small fixed-window admission limit to control
// requests. It intentionally counts rejected authorization attempts as well,
// preventing an authenticated client from using the database as a request
// amplification endpoint.
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

// removeLeaseLocked removes a lease and updates per-device accounting. The
// caller must hold c.mu.
func (c *Coordinator) removeLeaseLocked(id uint64) *Lease {
	lease := c.leases[id]
	if lease == nil {
		return nil
	}
	delete(c.leases, id)
	for _, deviceID := range []string{lease.ControllerID, lease.TargetID} {
		if count := c.leaseCounts[deviceID]; count <= 1 {
			delete(c.leaseCounts, deviceID)
		} else {
			c.leaseCounts[deviceID] = count - 1
		}
	}
	return lease
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

func rdpError(code, message string) protocol.RDPControlMessage {
	return protocol.RDPControlMessage{Type: protocol.RDPControlError, ErrorCode: code, ErrorMessage: message}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
