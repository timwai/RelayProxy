// Package desktop coordinates authenticated Relay Desktop sessions.
package desktop

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
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

const (
	DefaultLease = 60 * time.Second
	MinLease     = 15 * time.Second
	MaxLease     = 5 * time.Minute

	maxActiveLeases      = 1024
	maxLeasesPerDevice   = 64
	maxConnectsPerMinute = 120
	connectRateWindow    = time.Minute
)

type AuthorizeFunc func(controllerDeviceID, targetDeviceID string) (bool, error)

type Registration struct {
	DeviceID     string
	Capabilities protocol.DesktopCapabilities
	UpdatedAt    time.Time
}

type Lease struct {
	ID           uint64
	ControllerID string
	TargetID     string
	Token        []byte
	Options      protocol.RemoteDesktopConnectOptions
	ExpiresAt    time.Time
}

type connectWindow struct {
	started time.Time
	count   int
}

type Coordinator struct {
	sessions       *session.Manager
	authorize      AuthorizeFunc
	lease          time.Duration
	mu             sync.Mutex
	registrations  map[string]Registration
	leases         map[uint64]*Lease
	leaseCounts    map[string]int
	connectWindows map[string]connectWindow
	cancel         context.CancelFunc
	started        bool
}

func NewCoordinator(sessions *session.Manager, authorize AuthorizeFunc, lease time.Duration) *Coordinator {
	if lease <= 0 {
		lease = DefaultLease
	}
	if lease < MinLease {
		lease = MinLease
	}
	if lease > MaxLease {
		lease = MaxLease
	}
	return &Coordinator{
		sessions: sessions, authorize: authorize, lease: lease,
		registrations: make(map[string]Registration), leases: make(map[uint64]*Lease),
		leaseCounts: make(map[string]int), connectWindows: make(map[string]connectWindow),
	}
}

func (c *Coordinator) LeaseSeconds() int { return int(c.lease / time.Second) }

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
		c.notifyLease(lease, protocol.DesktopControlMessage{
			Type: protocol.DesktopControlSessionClose, SessionID: lease.ID,
			ControllerID: lease.ControllerID, TargetID: lease.TargetID, ErrorCode: "SERVER_SHUTDOWN",
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
			var expired []*Lease
			c.mu.Lock()
			for id, window := range c.connectWindows {
				if now.Sub(window.started) >= connectRateWindow {
					delete(c.connectWindows, id)
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
				c.notifyLease(lease, protocol.DesktopControlMessage{
					Type: protocol.DesktopControlSessionClose, SessionID: lease.ID,
					ControllerID: lease.ControllerID, TargetID: lease.TargetID, ErrorCode: "LEASE_EXPIRED",
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
	var message protocol.DesktopControlMessage
	if err := protocol.ReadJSON(stream, &message); err != nil {
		return
	}
	var response protocol.DesktopControlMessage
	switch message.Type {
	case protocol.DesktopControlCapabilities:
		response = c.register(device, message)
	case protocol.DesktopControlConnectRequest:
		response = c.connect(device, message)
	case protocol.DesktopControlLeaseRenew:
		response = c.renew(device.DeviceID, message)
	case protocol.DesktopControlSessionClose:
		if !c.validLeasePeer(device.DeviceID, message.SessionID, message.SessionToken) {
			response = desktopError("SESSION_TOKEN_INVALID", "Relay Desktop session token is invalid")
		} else {
			c.closeLease(device.DeviceID, message.SessionID, "PEER_CLOSED")
			response = protocol.DesktopControlMessage{Type: protocol.DesktopControlLeaseAck, SessionID: message.SessionID}
		}
	case protocol.DesktopControlConfig, protocol.DesktopControlStats, protocol.DesktopControlIDRRequest, protocol.DesktopControlPathChange:
		response = c.forward(device.DeviceID, message)
	default:
		response = desktopError("UNKNOWN_CONTROL", "unsupported Relay Desktop control message")
	}
	if response.Type != "" {
		_ = protocol.WriteJSON(stream, response)
	}
}

func (c *Coordinator) register(device *session.DeviceSession, message protocol.DesktopControlMessage) protocol.DesktopControlMessage {
	if !contains(device.Grants, protocol.CapabilityRDPHost) {
		return desktopError(protocol.ErrCodeAccessDenied, "device is not approved for desktop host access")
	}
	if message.Capabilities == nil || !message.Capabilities.RelayDesktop {
		return desktopError(protocol.ErrCodeInvalidRequest, "Relay Desktop host capabilities are required")
	}
	caps := cloneCapabilities(*message.Capabilities)
	c.mu.Lock()
	c.registrations[device.DeviceID] = Registration{DeviceID: device.DeviceID, Capabilities: caps, UpdatedAt: time.Now()}
	c.mu.Unlock()
	return protocol.DesktopControlMessage{Type: protocol.DesktopControlCapabilitiesAck, TargetID: device.DeviceID, LeaseSec: c.LeaseSeconds()}
}

func (c *Coordinator) connect(controller *session.DeviceSession, message protocol.DesktopControlMessage) protocol.DesktopControlMessage {
	if controller == nil || !contains(controller.Grants, protocol.CapabilityRDPClient) {
		return desktopError(protocol.ErrCodeAccessDenied, "device is not approved for desktop controller access")
	}
	if message.TargetID == "" || message.TargetID == controller.DeviceID {
		return desktopError("INVALID_TARGET", "Relay Desktop target is required")
	}
	now := time.Now()
	c.mu.Lock()
	if !c.allowConnectLocked(controller.DeviceID, now) {
		c.mu.Unlock()
		return desktopError(protocol.ErrCodeRateLimited, "too many Relay Desktop session requests")
	}
	c.mu.Unlock()
	if c.authorize == nil {
		return desktopError("AUTH_UNAVAILABLE", "Relay Desktop authorization is unavailable")
	}
	allowed, err := c.authorize(controller.DeviceID, message.TargetID)
	if err != nil {
		return desktopError("AUTH_UNAVAILABLE", err.Error())
	}
	if !allowed {
		return desktopError(protocol.ErrCodeAccessDenied, "controller is not authorized for this desktop target")
	}
	target, online := c.sessions.Get(message.TargetID)
	if !online || target == nil || !contains(target.Grants, protocol.CapabilityRDPHost) {
		return desktopError("TARGET_OFFLINE", "Relay Desktop target is offline")
	}
	c.mu.Lock()
	registration, registered := c.registrations[target.DeviceID]
	c.mu.Unlock()
	if !registered || !registration.Capabilities.RelayDesktop {
		return desktopError("TARGET_UNAVAILABLE", "Relay Desktop host backend is unavailable")
	}
	id, err := randomID()
	if err != nil {
		return desktopError("INTERNAL", "failed to allocate Relay Desktop session")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return desktopError("INTERNAL", "failed to allocate Relay Desktop session token")
	}
	options := protocol.RemoteDesktopConnectOptions{}
	if message.Options != nil {
		options = *message.Options
	}
	lease := &Lease{ID: id, ControllerID: controller.DeviceID, TargetID: target.DeviceID, Token: token, Options: options, ExpiresAt: now.Add(c.lease)}
	c.mu.Lock()
	if len(c.leases) >= maxActiveLeases || c.leaseCounts[controller.DeviceID] >= maxLeasesPerDevice || c.leaseCounts[target.DeviceID] >= maxLeasesPerDevice {
		c.mu.Unlock()
		return desktopError(protocol.ErrCodeConnectionLimit, "Relay Desktop session capacity reached")
	}
	c.leases[id] = lease
	c.leaseCounts[controller.DeviceID]++
	c.leaseCounts[target.DeviceID]++
	c.mu.Unlock()
	notify := protocol.DesktopControlMessage{
		Type: protocol.DesktopControlConnectNotify, SessionID: id,
		ControllerID: controller.DeviceID, TargetID: target.DeviceID,
		SessionToken: append([]byte(nil), token...), Options: &options, LeaseExpiresAt: lease.ExpiresAt.UnixMilli(),
	}
	if message.Capabilities != nil {
		caps := cloneCapabilities(*message.Capabilities)
		notify.Capabilities = &caps
	}
	if err := c.notify(target, notify); err != nil {
		c.closeLease("", id, "TARGET_NOTIFY_FAILED")
		return desktopError("TARGET_NOTIFY_FAILED", "failed to notify Relay Desktop target")
	}
	targetCaps := cloneCapabilities(registration.Capabilities)
	return protocol.DesktopControlMessage{
		Type: protocol.DesktopControlConnectResponse, SessionID: id,
		ControllerID: controller.DeviceID, TargetID: target.DeviceID,
		SessionToken: append([]byte(nil), token...), Capabilities: &targetCaps,
		Options: &options, LeaseExpiresAt: lease.ExpiresAt.UnixMilli(), LeaseSec: c.LeaseSeconds(),
	}
}

func (c *Coordinator) renew(deviceID string, message protocol.DesktopControlMessage) protocol.DesktopControlMessage {
	lease, ok := c.leaseForPeer(deviceID, message.SessionID, message.SessionToken)
	if !ok {
		return desktopError("SESSION_TOKEN_INVALID", "Relay Desktop session is no longer active or token is invalid")
	}
	if !c.reauthorize(lease) {
		c.closeLease("", lease.ID, "AUTH_REVOKED")
		return desktopError(protocol.ErrCodeAccessDenied, "Relay Desktop authorization was revoked")
	}
	c.mu.Lock()
	current, ok := c.leases[lease.ID]
	if ok && bytes.Equal(current.Token, message.SessionToken) {
		current.ExpiresAt = time.Now().Add(c.lease)
	}
	expires := int64(0)
	if ok {
		expires = current.ExpiresAt.UnixMilli()
	}
	c.mu.Unlock()
	if !ok {
		return desktopError("SESSION_NOT_FOUND", "Relay Desktop session is no longer active")
	}
	return protocol.DesktopControlMessage{Type: protocol.DesktopControlLeaseAck, SessionID: lease.ID, LeaseExpiresAt: expires}
}

func (c *Coordinator) forward(deviceID string, message protocol.DesktopControlMessage) protocol.DesktopControlMessage {
	lease, ok := c.leaseForPeer(deviceID, message.SessionID, message.SessionToken)
	if !ok {
		return desktopError("SESSION_TOKEN_INVALID", "Relay Desktop session token is invalid")
	}
	if !c.reauthorize(lease) {
		c.closeLease("", lease.ID, "AUTH_REVOKED")
		return desktopError(protocol.ErrCodeAccessDenied, "Relay Desktop authorization was revoked")
	}
	peerID := lease.TargetID
	if deviceID == lease.TargetID {
		peerID = lease.ControllerID
	}
	peer, online := c.sessions.Get(peerID)
	if !online || peer == nil {
		return desktopError("PEER_OFFLINE", "Relay Desktop peer is offline")
	}
	message.ControllerID = lease.ControllerID
	message.TargetID = lease.TargetID
	message.SessionToken = append([]byte(nil), lease.Token...)
	message.LeaseExpiresAt = lease.ExpiresAt.UnixMilli()
	if err := c.notify(peer, message); err != nil {
		return desktopError("PEER_NOTIFY_FAILED", "failed to forward Relay Desktop control message")
	}
	responseType := protocol.DesktopControlLeaseAck
	if message.Type == protocol.DesktopControlConfig {
		responseType = protocol.DesktopControlConfigAck
	}
	return protocol.DesktopControlMessage{Type: responseType, SessionID: lease.ID, LeaseExpiresAt: lease.ExpiresAt.UnixMilli()}
}

func (c *Coordinator) ValidateMedia(controllerID, targetID string, sessionID uint64, token []byte) (bool, error) {
	c.mu.Lock()
	lease, ok := c.leases[sessionID]
	if ok && (!time.Now().Before(lease.ExpiresAt) || lease.ControllerID != controllerID || lease.TargetID != targetID || !bytes.Equal(lease.Token, token)) {
		ok = false
	}
	var snapshot *Lease
	if ok {
		copyLease := *lease
		copyLease.Token = append([]byte(nil), lease.Token...)
		snapshot = &copyLease
	}
	c.mu.Unlock()
	if !ok {
		return false, nil
	}
	if c.authorize == nil {
		return false, errors.New("Relay Desktop authorization is unavailable")
	}
	allowed, err := c.authorize(snapshot.ControllerID, snapshot.TargetID)
	if err != nil {
		return false, err
	}
	if !allowed {
		c.closeLease("", snapshot.ID, "AUTH_REVOKED")
		return false, nil
	}
	return true, nil
}

func (c *Coordinator) Capabilities(deviceID string) (protocol.DesktopCapabilities, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	registration, ok := c.registrations[deviceID]
	if !ok {
		return protocol.DesktopCapabilities{}, false
	}
	return cloneCapabilities(registration.Capabilities), true
}

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
		c.notifyLease(lease, protocol.DesktopControlMessage{
			Type: protocol.DesktopControlSessionClose, SessionID: lease.ID,
			ControllerID: lease.ControllerID, TargetID: lease.TargetID, ErrorCode: "DEVICE_REVOKED_OR_OFFLINE",
		})
	}
}

func (c *Coordinator) leaseForPeer(deviceID string, id uint64, token []byte) (*Lease, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lease, ok := c.leases[id]
	if !ok || !time.Now().Before(lease.ExpiresAt) || (deviceID != lease.ControllerID && deviceID != lease.TargetID) || !bytes.Equal(token, lease.Token) {
		return nil, false
	}
	copyLease := *lease
	copyLease.Token = append([]byte(nil), lease.Token...)
	return &copyLease, true
}

func (c *Coordinator) validLeasePeer(deviceID string, id uint64, token []byte) bool {
	_, ok := c.leaseForPeer(deviceID, id, token)
	return ok
}

func (c *Coordinator) reauthorize(lease *Lease) bool {
	if lease == nil || c.authorize == nil {
		return false
	}
	allowed, err := c.authorize(lease.ControllerID, lease.TargetID)
	return err == nil && allowed
}

func (c *Coordinator) notify(device *session.DeviceSession, message protocol.DesktopControlMessage) error {
	if device == nil || device.Tunnel == nil {
		return errors.New("Relay Desktop peer is offline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := device.Tunnel.OpenStream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeDesktopControl,
		RequestID: fmt.Sprintf("desktop-%d", message.SessionID), ExitDeviceID: device.DeviceID,
	}); err != nil {
		return err
	}
	return protocol.WriteJSON(stream, message)
}

func (c *Coordinator) notifyLease(lease *Lease, message protocol.DesktopControlMessage) {
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
	c.notifyLease(lease, protocol.DesktopControlMessage{
		Type: protocol.DesktopControlSessionClose, SessionID: lease.ID,
		ControllerID: lease.ControllerID, TargetID: lease.TargetID,
		SessionToken: append([]byte(nil), lease.Token...), ErrorCode: reason,
	})
}

func (c *Coordinator) removeLeaseLocked(id uint64) *Lease {
	lease := c.leases[id]
	if lease == nil {
		return nil
	}
	delete(c.leases, id)
	for _, deviceID := range []string{lease.ControllerID, lease.TargetID} {
		if c.leaseCounts[deviceID] <= 1 {
			delete(c.leaseCounts, deviceID)
		} else {
			c.leaseCounts[deviceID]--
		}
	}
	return lease
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

func randomID() (uint64, error) {
	for {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return 0, err
		}
		if id := binary.BigEndian.Uint64(raw[:]); id != 0 {
			return id, nil
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cloneCapabilities(in protocol.DesktopCapabilities) protocol.DesktopCapabilities {
	out := in
	out.Captures = append([]protocol.DesktopCaptureCapability(nil), in.Captures...)
	out.Codecs = append([]protocol.DesktopCodecCapability(nil), in.Codecs...)
	out.Displays = append([]protocol.DesktopDisplayCapability(nil), in.Displays...)
	return out
}

func desktopError(code, message string) protocol.DesktopControlMessage {
	return protocol.DesktopControlMessage{Type: protocol.DesktopControlError, ErrorCode: code, ErrorMessage: message}
}
