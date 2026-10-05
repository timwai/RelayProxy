package direct

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	internaldirect "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
)

var (
	ErrAuthenticatorRequired = errors.New("public direct authenticator is required")
	ErrUnauthorized          = errors.New("public direct authentication failed")
)

// Authenticator validates the authorization ticket for one new Public Direct
// QUIC connection before any proxy stream can be accepted.
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

type AuthorizationRevisionResolver func(clientDeviceID string) (int64, bool)

type TicketAuthenticator struct {
	VerifyKey             []byte
	ExitDeviceID          string
	PolicyRevision        int64
	AuthorizationRevision AuthorizationRevisionResolver
	Now                   func() time.Time

	mu   sync.Mutex
	used map[string]int64
}

func (a *TicketAuthenticator) Authenticate(_ context.Context, request protocol.PublicDirectAuthRequest) error {
	if a == nil || len(a.VerifyKey) == 0 {
		return ErrAuthenticatorRequired
	}
	exitDeviceID := strings.TrimSpace(a.ExitDeviceID)
	if exitDeviceID == "" || strings.TrimSpace(request.ClientDeviceID) == "" ||
		strings.TrimSpace(request.ExitDeviceID) != exitDeviceID || len(request.Ticket) == 0 {
		return ErrUnauthorized
	}

	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	if a.AuthorizationRevision == nil {
		return ErrUnauthorized
	}
	authorizationRevision, ok := a.AuthorizationRevision(strings.TrimSpace(request.ClientDeviceID))
	if !ok {
		return ErrUnauthorized
	}

	claims, err := internaldirect.VerifyAccessTicket(a.VerifyKey, request.Ticket, internaldirect.TicketVerifyOptions{
		ClientDeviceID:        strings.TrimSpace(request.ClientDeviceID),
		ExitDeviceID:          exitDeviceID,
		PolicyRevision:        a.PolicyRevision,
		AuthorizationRevision: authorizationRevision,
		RequiredCapability:    internaldirect.AccessCapabilityProxy,
		Now:                   now,
	})
	if err != nil {
		return ErrUnauthorized
	}
	if !a.consumeNonce(claims.Nonce, claims.ExpiresAt, now.Unix()) {
		return ErrUnauthorized
	}
	return nil
}

func (a *TicketAuthenticator) consumeNonce(nonce []byte, expiresAt, now int64) bool {
	key := base64.RawURLEncoding.EncodeToString(nonce)
	if key == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.used == nil {
		a.used = make(map[string]int64)
	}
	for used, expiry := range a.used {
		if expiry <= now {
			delete(a.used, used)
		}
	}
	if expiry, exists := a.used[key]; exists && expiry > now {
		return false
	}
	a.used[key] = expiresAt
	return true
}
