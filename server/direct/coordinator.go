package direct

import (
	"context"
	"errors"
	"sync"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type Coordinator struct {
	registry  *Registry
	verifier  *Verifier
	onChanged func(deviceID string)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewCoordinator(registry *Registry, verifier *Verifier, onChanged func(deviceID string)) *Coordinator {
	if registry == nil {
		registry = NewRegistry(5 * time.Minute)
	}
	if verifier == nil {
		verifier = NewVerifier(5 * time.Second)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{
		registry: registry, verifier: verifier, onChanged: onChanged,
		ctx: ctx, cancel: cancel,
	}
}

func (c *Coordinator) Close() {
	if c == nil {
		return
	}
	c.cancel()
	c.wg.Wait()
}

func (c *Coordinator) HandleControl(ctx context.Context, stream tunnel.TunnelStream, owner *session.DeviceSession) {
	if c == nil || stream == nil {
		return
	}
	var message protocol.PublicDirectControlMessage
	if err := protocol.ReadJSON(stream, &message); err != nil {
		c.writeError(stream, "INVALID_REQUEST", "invalid Public Direct control message")
		return
	}
	if owner == nil || !owner.IsExit() {
		c.writeError(stream, protocol.ErrCodeAccessDenied, "Public Direct registration requires an authenticated Exit grant")
		return
	}
	switch message.Type {
	case protocol.PublicDirectControlRegister:
		c.handleRegister(stream, owner, message)
	case protocol.PublicDirectControlUnregister:
		removed := c.registry.InvalidateSession(owner)
		if removed {
			c.changed(owner.DeviceID)
		}
		_ = protocol.WriteJSON(stream, protocol.PublicDirectControlMessage{
			Type:           protocol.PublicDirectControlUnregisterAck,
			RegistrationID: message.RegistrationID,
		})
	default:
		c.writeError(stream, "INVALID_REQUEST", "unsupported Public Direct control operation")
	}
}

func (c *Coordinator) handleRegister(
	stream tunnel.TunnelStream,
	owner *session.DeviceSession,
	message protocol.PublicDirectControlMessage,
) {
	targets, err := c.registry.Register(
		owner,
		message.RegistrationID,
		message.VerificationSecret,
		message.ListenerPort,
		message.Candidates,
	)
	if err != nil {
		c.writeError(stream, "INVALID_REQUEST", err.Error())
		return
	}
	snapshot, _ := c.registry.Snapshot(owner.DeviceID)
	if err := protocol.WriteJSON(stream, protocol.PublicDirectControlMessage{
		Type:           protocol.PublicDirectControlRegisterAck,
		RegistrationID: message.RegistrationID,
		ExpiresAt:      snapshot.ExpiresAt.Unix(),
	}); err != nil {
		c.registry.InvalidateSession(owner)
		return
	}

	for _, target := range targets {
		target := target
		c.registry.MarkVerifying(target)
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			if err := c.verifier.Verify(c.ctx, target); err != nil {
				c.registry.MarkFailed(target, err)
				return
			}
			if c.registry.MarkVerified(target) {
				c.changed(target.DeviceID)
			}
		}()
	}
}

func (c *Coordinator) VerifiedEndpoints(deviceID string) []protocol.PublicDirectEndpoint {
	if c == nil || c.registry == nil {
		return nil
	}
	return c.registry.VerifiedEndpoints(deviceID)
}

func (c *Coordinator) Snapshot(deviceID string) (RegistrationSnapshot, bool) {
	if c == nil || c.registry == nil {
		return RegistrationSnapshot{}, false
	}
	return c.registry.Snapshot(deviceID)
}

func (c *Coordinator) RevokeDevice(deviceID string) {
	if c == nil || c.registry == nil {
		return
	}
	if c.registry.RevokeDevice(deviceID) {
		c.changed(deviceID)
	}
}

func (c *Coordinator) RevokeSession(owner *session.DeviceSession) {
	if c == nil || c.registry == nil || owner == nil {
		return
	}
	if c.registry.InvalidateSession(owner) {
		c.changed(owner.DeviceID)
	}
}

func (c *Coordinator) changed(deviceID string) {
	if c.onChanged != nil && deviceID != "" {
		c.onChanged(deviceID)
	}
}

func (c *Coordinator) writeError(stream tunnel.TunnelStream, code, message string) {
	if stream == nil {
		return
	}
	if code == "" {
		code = protocol.ErrCodeInvalidRequest
	}
	if message == "" {
		message = errors.New("Public Direct control request failed").Error()
	}
	_ = protocol.WriteJSON(stream, protocol.PublicDirectControlMessage{
		Type:         protocol.PublicDirectControlError,
		ErrorCode:    code,
		ErrorMessage: message,
	})
}
