package direct

import (
	"context"
	"errors"

	"relayproxy/internal/protocol"
)

var (
	ErrAuthenticatorRequired = errors.New("public direct authenticator is required")
	ErrUnauthorized          = errors.New("public direct authentication failed")
)

// Authenticator validates the opaque authorization ticket for one new Public
// Direct QUIC connection. Production implementations are intentionally absent
// until the Server-signed ticket work in Phase 4 is complete.
type Authenticator interface {
	Authenticate(context.Context, protocol.PublicDirectAuthRequest) error
}

type AuthenticatorFunc func(context.Context, protocol.PublicDirectAuthRequest) error

func (f AuthenticatorFunc) Authenticate(ctx context.Context, request protocol.PublicDirectAuthRequest) error {
	if f == nil {
		return ErrAuthenticatorRequired
	}
	return f(ctx, request)
}
