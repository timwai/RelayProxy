package gateway

import (
	"context"
	"errors"
	"log"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

func (r *StreamRouter) authorizeRDP(controller, target string) (*session.DeviceSession, error) {
	if !containsCapability(controllerGrants(r.sessions, controller), protocol.CapabilityRDPClient) {
		return nil, errors.New("device is not approved for RDP controller access")
	}
	targetSession, ok := r.sessions.Get(target)
	if !ok || targetSession == nil || !containsCapability(targetSession.Grants, protocol.CapabilityRDPHost) {
		return nil, errors.New("RDP target is offline or not approved")
	}
	if r.rdpChecker == nil {
		return nil, errors.New("RDP authorization is not configured")
	}
	ok, err := r.rdpChecker(controller, target)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("controller is not authorized for this RDP target")
	}
	return targetSession, nil
}

func controllerGrants(manager *session.Manager, deviceID string) []string {
	if manager == nil {
		return nil
	}
	if current, ok := manager.Get(deviceID); ok && current != nil {
		return current.Grants
	}
	return nil
}

func writeRDPDenied(stream tunnel.TunnelStream, requestID, message string) {
	_ = protocol.WriteJSON(stream, protocol.OpenTCPResponse{
		RequestID: requestID, Success: false,
		ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: message,
	})
}

func (r *StreamRouter) handleOpenRDPTCP(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.OpenRDPRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		return
	}
	target, err := r.authorizeRDP(clientSession.DeviceID, header.ExitDeviceID)
	if err != nil {
		writeRDPDenied(clientStream, req.RequestID, err.Error())
		r.emitAudit(&repository.ConnectionAudit{ClientDeviceID: clientSession.DeviceID, ExitDeviceID: header.ExitDeviceID, Protocol: "rdp", Result: "UNAUTHORIZED", ErrorCode: protocol.ErrCodeAccessDenied, StartedAt: time.Now(), EndedAt: time.Now()})
		return
	}

	deadline := handshakeDeadline
	if req.TimeoutMs > 0 {
		candidate := time.Now().Add(time.Duration(req.TimeoutMs) * time.Millisecond)
		if candidate.Before(deadline) {
			deadline = candidate
		}
	}
	openCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	targetStream, err := target.Tunnel.OpenStream(openCtx)
	if err != nil {
		writeRDPDenied(clientStream, req.RequestID, "failed to open RDP target stream")
		return
	}
	defer targetStream.Close()
	defer tunnel.InterruptOnCancel(ctx, targetStream)()
	_ = targetStream.SetDeadline(deadline)
	header.Type = protocol.FrameTypeOpenRDP
	if err := protocol.WriteStreamHeader(targetStream, header); err != nil {
		return
	}
	if err := protocol.WriteJSON(targetStream, req); err != nil {
		return
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(targetStream, &response); err != nil {
		writeRDPDenied(clientStream, req.RequestID, "RDP target did not respond")
		return
	}
	_ = targetStream.SetDeadline(time.Time{})
	if err := protocol.WriteJSON(clientStream, response); err != nil || !response.Success {
		return
	}
	_ = clientStream.SetDeadline(time.Time{})
	clientSession.ActiveStreams.Add(1)
	target.ActiveStreams.Add(1)
	defer clientSession.ActiveStreams.Add(-1)
	defer target.ActiveStreams.Add(-1)
	up, down := r.pipeStreams(ctx, clientStream, targetStream, clientSession, target)
	r.emitAudit(&repository.ConnectionAudit{ClientDeviceID: clientSession.DeviceID, ExitDeviceID: target.DeviceID, Protocol: "rdp", TargetHost: "127.0.0.1", TargetPort: 3389, StartedAt: time.Now(), EndedAt: time.Now(), BytesUp: up, BytesDown: down, Result: "SUCCESS"})
}

func (r *StreamRouter) handleOpenRDPUDP(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.OpenRDPRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		return
	}
	target, err := r.authorizeRDP(clientSession.DeviceID, header.ExitDeviceID)
	if err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeAccessDenied, ErrorMessage: err.Error()})
		return
	}
	if req.Mode != protocol.UDPModeDatagram || req.AssociationID == 0 || !tunnel.PeerSupportsDatagrams(clientSession.Tunnel) || !tunnel.PeerSupportsDatagrams(target.Tunnel) {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: "RDP UDP requires native QUIC datagrams on both tunnel legs"})
		return
	}
	clientDatagrams, err := tunnel.OpenDatagramChannel(clientSession.Tunnel, req.AssociationID)
	if err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: err.Error()})
		return
	}
	defer clientDatagrams.Close()
	targetDatagrams, err := tunnel.OpenDatagramChannel(target.Tunnel, 0)
	if err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeDatagramRequired, ErrorMessage: err.Error()})
		return
	}
	defer targetDatagrams.Close()

	deadline := handshakeDeadline
	if req.TimeoutMs > 0 {
		candidate := time.Now().Add(time.Duration(req.TimeoutMs) * time.Millisecond)
		if candidate.Before(deadline) {
			deadline = candidate
		}
	}
	openCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	targetStream, err := target.Tunnel.OpenStream(openCtx)
	if err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeStreamOpenFailed, ErrorMessage: "failed to open RDP target UDP stream"})
		return
	}
	defer targetStream.Close()
	defer tunnel.InterruptOnCancel(ctx, targetStream)()
	_ = targetStream.SetDeadline(deadline)
	header.Type = protocol.FrameTypeOpenRDPUDP
	req.AssociationID = targetDatagrams.ID
	if err := protocol.WriteStreamHeader(targetStream, header); err != nil {
		return
	}
	if err := protocol.WriteJSON(targetStream, req); err != nil {
		return
	}
	var response protocol.OpenUDPResponse
	if err := protocol.ReadJSON(targetStream, &response); err != nil {
		_ = protocol.WriteJSON(clientStream, protocol.OpenUDPResponse{RequestID: req.RequestID, ErrorCode: protocol.ErrCodeConnectTimeout, ErrorMessage: "RDP target did not respond to UDP negotiation"})
		return
	}
	_ = targetStream.SetDeadline(time.Time{})
	if !response.Success || response.Mode != protocol.UDPModeDatagram || response.AssociationID != targetDatagrams.ID {
		if response.ErrorCode == "" {
			response.ErrorCode = protocol.ErrCodeDatagramRequired
		}
		_ = protocol.WriteJSON(clientStream, response)
		return
	}
	response.AssociationID = clientDatagrams.ID
	if err := protocol.WriteJSON(clientStream, response); err != nil {
		return
	}
	_ = clientStream.SetDeadline(time.Time{})
	clientSession.ActiveStreams.Add(1)
	target.ActiveStreams.Add(1)
	defer clientSession.ActiveStreams.Add(-1)
	defer target.ActiveStreams.Add(-1)
	// RDP sessions commonly remain visually idle for more than the generic UDP
	// timeout. Their authenticated lifetime streams already provide exact
	// cleanup, so keep the datagram association until either endpoint closes.
	up, down := r.pipeDatagrams(ctx, clientStream, targetStream, clientDatagrams, targetDatagrams, clientSession, target, 0)
	log.Printf("[RDP] UDP relay closed controller=%s target=%s up=%d down=%d", clientSession.DeviceID, target.DeviceID, up, down)
}
