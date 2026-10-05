package direct

import (
	"context"
	"errors"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func RequestTicket(ctx context.Context, relay tunnel.TunnelSession, exitDeviceID string) (protocol.PublicDirectTicketResponse, error) {
	if relay == nil {
		return protocol.PublicDirectTicketResponse{}, errors.New("public direct ticket request requires an authenticated relay session")
	}
	stream, err := relay.OpenStream(ctx)
	if err != nil {
		return protocol.PublicDirectTicketResponse{}, err
	}
	defer stream.Close()
	stopCancel := tunnel.InterruptOnCancel(ctx, stream)
	defer stopCancel()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	} else {
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))
	}
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion,
		Type: protocol.FrameTypePublicDirectTicket, RequestID: "public_direct_ticket",
	}); err != nil {
		return protocol.PublicDirectTicketResponse{}, err
	}
	if err := protocol.WriteJSON(stream, protocol.PublicDirectTicketRequest{ExitDeviceID: exitDeviceID}); err != nil {
		return protocol.PublicDirectTicketResponse{}, err
	}
	var response protocol.PublicDirectTicketResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return protocol.PublicDirectTicketResponse{}, err
	}
	if !response.Success {
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeAccessDenied
		}
		message := response.ErrorMessage
		if message == "" {
			message = "public direct ticket request failed"
		}
		return response, protocol.NewRelayError(code, message)
	}
	if len(response.Ticket) == 0 || response.ExpiresAt <= time.Now().Unix() {
		return protocol.PublicDirectTicketResponse{}, errors.New("server returned an invalid public direct ticket")
	}
	stopCancel()
	_ = stream.SetDeadline(time.Time{})
	return response, nil
}
