package direct

import (
	"context"
	"errors"

	"relayproxy/internal/acl"
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

// PolicyAuthenticator is the production authentication contract. In addition
// to validating the ticket it returns the Server-authoritative Relay ACL that
// must be bound to every proxy stream on the accepted direct session.
type Authorization struct {
	RelayPolicy       *acl.Policy
	BrutalUploadBPS   uint64
	BrutalDownloadBPS uint64
}

// AuthorizationAuthenticator returns the current Server-authoritative policy
// and performance profile in one validation round-trip.
type AuthorizationAuthenticator interface {
	Authenticator
	AuthenticateAuthorization(context.Context, protocol.PublicDirectAuthRequest) (Authorization, error)
}

type PolicyAuthenticator interface {
	Authenticator
	AuthenticatePolicy(context.Context, protocol.PublicDirectAuthRequest) (*acl.Policy, error)
}

type AuthenticatorFunc func(context.Context, protocol.PublicDirectAuthRequest) error

func (f AuthenticatorFunc) Authenticate(ctx context.Context, request protocol.PublicDirectAuthRequest) error {
	if f == nil {
		return ErrAuthenticatorRequired
	}
	return f(ctx, request)
}
