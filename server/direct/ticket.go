package direct

import (
	"context"
	"strings"

	internaldirect "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type TicketAuthorizationContext struct {
	PolicyRevision        int64
	AuthorizationRevision int64
}

type TicketAuthorizeFunc func(clientDeviceID, exitDeviceID string) (TicketAuthorizationContext, bool, error)

type TicketAuthorizationSyncFunc func(context.Context, string, protocol.PublicDirectAuthorizationUpdate) error

type TicketIssuer struct {
	Registry  *Registry
	Signer    *internaldirect.TicketSigner
	Authorize TicketAuthorizeFunc
	Sync      TicketAuthorizationSyncFunc
}

func (i *TicketIssuer) PublicKey() []byte {
	if i == nil || i.Signer == nil {
		return nil
	}
	return i.Signer.PublicKey()
}

func (i *TicketIssuer) HandleControl(ctx context.Context, stream tunnel.TunnelStream, dev *session.DeviceSession) {
	if stream == nil {
		return
	}
	defer stream.Close()

	if i == nil || i.Registry == nil || i.Signer == nil || i.Authorize == nil || i.Sync == nil ||
		dev == nil || strings.TrimSpace(dev.DeviceID) == "" ||
		!containsValue(dev.Grants, protocol.CapabilityProxyClient) {
		_ = writeTicketError(stream, protocol.ErrCodeAccessDenied, "public direct ticket issuance is unavailable")
		return
	}

	var request protocol.PublicDirectTicketRequest
	if err := protocol.ReadJSON(stream, &request); err != nil {
		_ = writeTicketError(stream, protocol.ErrCodeInvalidRequest, "invalid public direct ticket request")
		return
	}
	request.ExitDeviceID = strings.TrimSpace(request.ExitDeviceID)
	if request.ExitDeviceID == "" || request.ExitDeviceID == dev.DeviceID {
		_ = writeTicketError(stream, protocol.ErrCodeInvalidRequest, "invalid public direct exit")
		return
	}
	if len(i.Registry.VerifiedEndpoints(request.ExitDeviceID)) == 0 ||
		i.Registry.VerifiedCertificateFingerprint(request.ExitDeviceID) == "" {
		_ = writeTicketError(stream, protocol.ErrCodeAccessDenied, "public direct endpoint is not verified")
		return
	}

	authorization, allowed, err := i.Authorize(dev.DeviceID, request.ExitDeviceID)
	if err != nil {
		_ = writeTicketError(stream, protocol.ErrCodeAccessDenied, "public direct authorization check failed")
		return
	}
	if !allowed {
		_ = writeTicketError(stream, protocol.ErrCodeAccessDenied, "public direct access is not authorized")
		return
	}
	if err := i.Sync(ctx, request.ExitDeviceID, protocol.PublicDirectAuthorizationUpdate{
		ClientDeviceID:        dev.DeviceID,
		ExitDeviceID:          request.ExitDeviceID,
		PolicyRevision:        authorization.PolicyRevision,
		AuthorizationRevision: authorization.AuthorizationRevision,
		Authorized:            true,
	}); err != nil {
		_ = writeTicketError(stream, protocol.ErrCodeAccessDenied, "public direct authorization state is not synchronized")
		return
	}

	raw, claims, err := i.Signer.Issue(
		dev.DeviceID,
		request.ExitDeviceID,
		authorization.PolicyRevision,
		authorization.AuthorizationRevision,
		[]string{internaldirect.AccessCapabilityProxy},
	)
	if err != nil {
		_ = writeTicketError(stream, protocol.ErrCodeInternalError, "public direct ticket issuance failed")
		return
	}
	_ = protocol.WriteJSON(stream, protocol.PublicDirectTicketResponse{
		Success:               true,
		Ticket:                raw,
		ExpiresAt:             claims.ExpiresAt,
		PolicyRevision:        claims.PolicyRevision,
		AuthorizationRevision: claims.AuthorizationRevision,
	})
}

func writeTicketError(stream tunnel.TunnelStream, code, message string) error {
	return protocol.WriteJSON(stream, protocol.PublicDirectTicketResponse{
		Success: false, ErrorCode: code, ErrorMessage: message,
	})
}

func containsValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
