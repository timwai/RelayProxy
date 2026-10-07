package direct

import (
	"context"
	"fmt"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func RegisterEndpoint(ctx context.Context, relay tunnel.TunnelSession, request protocol.PublicDirectRegistrationRequest) (protocol.PublicDirectRegistrationResponse, error) {
	request.Operation = protocol.PublicDirectControlRegister
	return sendPublicDirectControl(ctx, relay, "public_direct_register", request, "public direct endpoint registration failed")
}

func ValidateTicketCurrent(ctx context.Context, relay tunnel.TunnelSession, claims protocol.PublicDirectTicketClaims) (Authorization, error) {
	request := protocol.PublicDirectRegistrationRequest{
		Operation: protocol.PublicDirectControlValidateTicket,
		TicketValidation: &protocol.PublicDirectTicketValidationRequest{
			ClientDeviceID:        claims.ClientDeviceID,
			ExitDeviceID:          claims.ExitDeviceID,
			PolicyRevision:        claims.PolicyRevision,
			AuthorizationRevision: claims.AuthorizationRevision,
		},
	}
	response, err := sendPublicDirectControl(ctx, relay, "public_direct_validate_ticket", request, "public direct authorization is no longer current")
	if err != nil {
		return Authorization{}, err
	}
	if response.RelayPolicy == nil || response.RelayPolicy.Fingerprint == "" {
		return Authorization{}, fmt.Errorf("public direct validation response is missing the relay ACL")
	}
	checker, err := acl.NewChecker(*response.RelayPolicy)
	if err != nil {
		return Authorization{}, fmt.Errorf("invalid public direct relay ACL: %w", err)
	}
	policy := checker.Policy()
	if policy.Fingerprint != response.RelayPolicy.Fingerprint {
		return Authorization{}, fmt.Errorf("public direct relay ACL fingerprint mismatch")
	}
	return Authorization{
		RelayPolicy: &policy,
		BrutalUploadBPS: response.BrutalUploadBPS,
		BrutalDownloadBPS: response.BrutalDownloadBPS,
	}, nil
}

func sendPublicDirectControl(
	ctx context.Context,
	relay tunnel.TunnelSession,
	requestID string,
	request protocol.PublicDirectRegistrationRequest,
	defaultMessage string,
) (protocol.PublicDirectRegistrationResponse, error) {
	if relay == nil {
		return protocol.PublicDirectRegistrationResponse{}, fmt.Errorf("public direct control requires an authenticated relay session")
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
		Type: protocol.FrameTypePublicDirectControl, RequestID: requestID,
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
			message = defaultMessage
		}
		return response, protocol.NewRelayError(code, message)
	}
	stopCancel()
	_ = stream.SetDeadline(time.Time{})
	return response, nil
}
