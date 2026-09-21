package gateway

import (
	"context"
	"log"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

// handleOpenDesktopMedia creates two independent RD/1 native-datagram
// associations and forwards packets between them. RD1 deliberately reuses the
// existing owner-scoped RDP grant as admission authority until desktop-specific
// grants are exposed by the admin model.
func (r *StreamRouter) handleOpenDesktopMedia(ctx context.Context, header *protocol.StreamHeader, clientStream tunnel.TunnelStream, clientSession *session.DeviceSession, handshakeDeadline time.Time) {
	var req protocol.OpenDesktopMediaRequest
	if err := protocol.ReadJSON(clientStream, &req); err != nil {
		return
	}
	deny := func(code, message string) {
		_ = protocol.WriteJSON(clientStream, protocol.OpenDesktopMediaResponse{
			RequestID: req.RequestID, ErrorCode: code, ErrorMessage: message,
		})
	}
	target, err := r.authorizeRDP(clientSession.DeviceID, header.ExitDeviceID)
	if err != nil {
		deny(protocol.ErrCodeAccessDenied, err.Error())
		r.emitAudit(&repository.ConnectionAudit{
			UserID: clientSession.OwnerUserID, ClientDeviceID: clientSession.DeviceID, ExitDeviceID: header.ExitDeviceID,
			Protocol: "desktop-media", Result: "UNAUTHORIZED", ErrorCode: protocol.ErrCodeAccessDenied,
			StartedAt: time.Now(), EndedAt: time.Now(),
		})
		return
	}
	if req.Mode != protocol.DesktopMediaModeDatagram || req.AssociationID == 0 ||
		!tunnel.PeerSupportsDatagrams(clientSession.Tunnel) || !tunnel.PeerSupportsDatagrams(target.Tunnel) {
		deny(protocol.ErrCodeDatagramRequired, "Relay Desktop media requires QUIC Datagram on both tunnel legs")
		return
	}

	clientDatagrams, err := tunnel.OpenDesktopDatagramChannel(clientSession.Tunnel, req.AssociationID)
	if err != nil {
		deny(protocol.ErrCodeDatagramRequired, err.Error())
		return
	}
	defer clientDatagrams.Close()
	targetDatagrams, err := tunnel.OpenDesktopDatagramChannel(target.Tunnel, 0)
	if err != nil {
		deny(protocol.ErrCodeDatagramRequired, err.Error())
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
		deny(protocol.ErrCodeStreamOpenFailed, "failed to open Relay Desktop target stream")
		return
	}
	defer targetStream.Close()
	defer tunnel.InterruptOnCancel(ctx, targetStream)()
	_ = targetStream.SetDeadline(deadline)
	header.Type = protocol.FrameTypeOpenDesktopMedia
	req.AssociationID = targetDatagrams.ID
	if err := protocol.WriteStreamHeader(targetStream, header); err != nil {
		return
	}
	if err := protocol.WriteJSON(targetStream, req); err != nil {
		return
	}

	var response protocol.OpenDesktopMediaResponse
	if err := protocol.ReadJSON(targetStream, &response); err != nil {
		deny(protocol.ErrCodeConnectTimeout, "Relay Desktop target did not respond")
		return
	}
	_ = targetStream.SetDeadline(time.Time{})
	if !response.Success || response.Mode != protocol.DesktopMediaModeDatagram || response.AssociationID != targetDatagrams.ID {
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

	start := time.Now()
	clientSession.ActiveStreams.Add(1)
	target.ActiveStreams.Add(1)
	defer clientSession.ActiveStreams.Add(-1)
	defer target.ActiveStreams.Add(-1)
	up, down := r.pipeDatagrams(ctx, clientStream, targetStream, clientDatagrams, targetDatagrams, clientSession, target, 0)
	log.Printf("[Desktop] media relay closed controller=%s target=%s up=%d down=%d", clientSession.DeviceID, target.DeviceID, up, down)
	r.emitAudit(&repository.ConnectionAudit{
		UserID: clientSession.OwnerUserID, ClientDeviceID: clientSession.DeviceID, ExitDeviceID: target.DeviceID,
		Protocol: "desktop-media", StartedAt: start, EndedAt: time.Now(), BytesUp: up, BytesDown: down, Result: "SUCCESS",
	})
}
