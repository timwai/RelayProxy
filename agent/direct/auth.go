package direct

import (
	"context"

	"relayproxy/internal/tunnel"
)

// SessionAuthenticator performs the application-layer authentication handshake
// for one Public Direct QUIC session. Phase 4 supplies the production ticket
// implementation. Callers must provide an authenticator; there is deliberately
// no insecure production default.
type SessionAuthenticator func(context.Context, tunnel.TunnelSession) error
