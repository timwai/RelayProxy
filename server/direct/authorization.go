package direct

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type AuthorizationSyncer struct {
	Sessions *session.Manager
	Timeout  time.Duration
}

func (s *AuthorizationSyncer) Push(ctx context.Context, exitDeviceID string, update protocol.PublicDirectAuthorizationUpdate) error {
	if s == nil || s.Sessions == nil {
		return errors.New("public direct authorization sync is unavailable")
	}
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if exitDeviceID == "" {
		return errors.New("public direct authorization sync requires an exit id")
	}
	exitSession, ok := s.Sessions.Get(exitDeviceID)
	if !ok || exitSession == nil || !exitSession.IsExit() || exitSession.Tunnel == nil {
		return errors.New("public direct exit is offline")
	}
	if !containsValue(exitSession.Capabilities, protocol.CapabilityProxyPublicDirect) {
		return errors.New("public direct exit does not support authorization sync")
	}
	update.ExitDeviceID = exitDeviceID
	if update.PolicyRevision != exitSession.PolicyRevision {
		return errors.New("public direct exit policy revision is stale")
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := exitSession.Tunnel.OpenStream(syncCtx)
	if err != nil {
		return fmt.Errorf("open public direct authorization sync stream: %w", err)
	}
	defer stream.Close()
	stopCancel := tunnel.InterruptOnCancel(syncCtx, stream)
	defer stopCancel()
	_ = stream.SetDeadline(time.Now().Add(timeout))

	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion,
		Type: protocol.FrameTypePublicDirectAuthorization, RequestID: "public_direct_authorization",
		ExitDeviceID: exitDeviceID,
	}); err != nil {
		return err
	}
	if err := protocol.WriteJSON(stream, update); err != nil {
		return err
	}
	var receipt protocol.PublicDirectAuthorizationReceipt
	if err := protocol.ReadJSON(stream, &receipt); err != nil {
		return err
	}
	if !receipt.Success {
		if receipt.ErrorMessage == "" {
			receipt.ErrorMessage = "public direct authorization update rejected"
		}
		return errors.New(receipt.ErrorMessage)
	}
	return nil
}

func (s *AuthorizationSyncer) RevokeClientFromAllExits(ctx context.Context, clientDeviceID string) {
	if s == nil || s.Sessions == nil {
		return
	}
	clientDeviceID = strings.TrimSpace(clientDeviceID)
	if clientDeviceID == "" {
		return
	}
	for _, exitSession := range s.Sessions.GetExits() {
		if exitSession == nil || exitSession.DeviceID == clientDeviceID ||
			!containsValue(exitSession.Capabilities, protocol.CapabilityProxyPublicDirect) {
			continue
		}
		err := s.Push(ctx, exitSession.DeviceID, protocol.PublicDirectAuthorizationUpdate{
			ClientDeviceID:        clientDeviceID,
			ExitDeviceID:          exitSession.DeviceID,
			PolicyRevision:        exitSession.PolicyRevision,
			AuthorizationRevision: 0,
			Authorized:            false,
		})
		if err != nil {
			// Fail closed: an Exit that cannot receive revocation state must not
			// keep serving Public Direct sessions under stale authorization.
			s.Sessions.Unregister(exitSession.DeviceID)
		}
	}
}

func (s *AuthorizationSyncer) RevokeClients(ctx context.Context, exitDeviceID string, clientDeviceIDs []string) error {
	if s == nil || s.Sessions == nil {
		return errors.New("public direct authorization sync is unavailable")
	}
	exitSession, ok := s.Sessions.Get(strings.TrimSpace(exitDeviceID))
	if !ok || exitSession == nil || !exitSession.IsExit() {
		return nil
	}
	var firstErr error
	for _, clientDeviceID := range clientDeviceIDs {
		clientDeviceID = strings.TrimSpace(clientDeviceID)
		if clientDeviceID == "" || clientDeviceID == exitSession.DeviceID {
			continue
		}
		err := s.Push(ctx, exitSession.DeviceID, protocol.PublicDirectAuthorizationUpdate{
			ClientDeviceID:        clientDeviceID,
			ExitDeviceID:          exitSession.DeviceID,
			PolicyRevision:        exitSession.PolicyRevision,
			AuthorizationRevision: 0,
			Authorized:            false,
		})
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
