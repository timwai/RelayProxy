package direct

import (
	"context"
	"fmt"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func RegisterEndpoint(ctx context.Context, relay tunnel.TunnelSession, request protocol.PublicDirectRegistrationRequest) (protocol.PublicDirectRegistrationResponse, error) {
	if relay == nil {
		return protocol.PublicDirectRegistrationResponse{}, fmt.Errorf("public direct registration requires an authenticated relay session")
	}
	stream, err := relay.OpenStream(ctx)
	if err != nil {
		return protocol.PublicDirectRegistrationResponse{}, err
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
		Type: protocol.FrameTypePublicDirectControl, RequestID: "public_direct_register",
	}); err != nil {
		return protocol.PublicDirectRegistrationResponse{}, err
	}
	if err := protocol.WriteJSON(stream, request); err != nil {
		return protocol.PublicDirectRegistrationResponse{}, err
	}
	var response protocol.PublicDirectRegistrationResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		return protocol.PublicDirectRegistrationResponse{}, err
	}
	if !response.Success {
		code := response.ErrorCode
		if code == "" {
			code = protocol.ErrCodeInvalidRequest
		}
		message := response.ErrorMessage
		if message == "" {
			message = "public direct endpoint registration failed"
		}
		return response, protocol.NewRelayError(code, message)
	}
	stopCancel()
	_ = stream.SetDeadline(time.Time{})
	return response, nil
}
