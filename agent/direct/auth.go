package direct

import (
	"context"
	"errors"
	"time"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

// SessionAuthenticator performs the application-layer authentication handshake
// for one Public Direct QUIC session. Phase 4 supplies the production ticket
// implementation. Callers must provide an authenticator; there is deliberately
// no insecure production default.
type SessionAuthenticator func(context.Context, tunnel.TunnelSession) error

var ErrVerificationComplete = errors.New("Public Direct verification session complete")

// VerificationAuthenticator accepts only one endpoint-verification handshake.
// It is safe to use before Direct Access Tickets exist because every non-probe
// session is rejected before proxy streams are dispatched.
func VerificationAuthenticator(registrationID string, secret []byte) SessionAuthenticator {
	secretCopy := directtransport.CloneSecret(secret)
	return func(ctx context.Context, session tunnel.TunnelSession) error {
		if registrationID == "" || len(secretCopy) != directtransport.VerificationSecretSize {
			return errors.New("Public Direct verification identity is not configured")
		}
		handshakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stream, err := session.AcceptStream(handshakeCtx)
		if err != nil {
			return err
		}
		defer stream.Close()
		_ = stream.SetDeadline(time.Now().Add(5 * time.Second))

		var request protocol.PublicDirectHandshake
		if err := protocol.ReadJSON(stream, &request); err != nil {
			return err
		}
		if request.Type != protocol.PublicDirectHandshakeVerify ||
			request.RegistrationID != registrationID {
			return errors.New("Public Direct verification request does not match listener registration")
		}
		proof, err := directtransport.VerificationProof(secretCopy, registrationID, request.Nonce)
		if err != nil {
			return err
		}
		if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshake{
			Type:           protocol.PublicDirectHandshakeVerifyResponse,
			RegistrationID: registrationID,
			Proof:          proof,
		}); err != nil {
			return err
		}
		_ = stream.CloseWrite()
		return ErrVerificationComplete
	}
}
