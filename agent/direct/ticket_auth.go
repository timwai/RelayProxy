package direct

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
)

const (
	maxSignedTicketSize = 4096
	maxReplayEntries    = 8192
	maxTicketLifetime   = 5 * time.Minute
	maxTicketClockSkew  = 30 * time.Second
)

var (
	ErrTicketExpired = errors.New("public direct ticket expired")
	ErrTicketReplay  = errors.New("public direct ticket replayed")
)

type TicketCurrentValidator func(context.Context, protocol.PublicDirectTicketClaims) (Authorization, error)

type TicketAuthenticator struct {
	issuer           string
	exitID           string
	publicKey        ed25519.PublicKey
	now              func() time.Time
	currentValidator TicketCurrentValidator

	mu     sync.Mutex
	replay map[string]int64
}

func NewTicketAuthenticator(issuer string, publicKey []byte, exitDeviceID string) (*TicketAuthenticator, error) {
	issuer = strings.TrimSpace(issuer)
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if issuer == "" || exitDeviceID == "" {
		return nil, errors.New("public direct ticket issuer and exit device id are required")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("public direct ticket verification key is invalid")
	}
	return &TicketAuthenticator{
		issuer: issuer, exitID: exitDeviceID,
		publicKey: append(ed25519.PublicKey(nil), publicKey...),
		now:       time.Now,
		replay:    make(map[string]int64),
	}, nil
}

func (a *TicketAuthenticator) SetCurrentValidator(validator TicketCurrentValidator) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.currentValidator = validator
	a.mu.Unlock()
}

func (a *TicketAuthenticator) Authenticate(ctx context.Context, request protocol.PublicDirectAuthRequest) error {
	_, err := a.authenticate(ctx, request, false)
	return err
}

func (a *TicketAuthenticator) AuthenticateAuthorization(ctx context.Context, request protocol.PublicDirectAuthRequest) (Authorization, error) {
	return a.authenticate(ctx, request, true)
}

func (a *TicketAuthenticator) AuthenticatePolicy(ctx context.Context, request protocol.PublicDirectAuthRequest) (*acl.Policy, error) {
	authorization, err := a.authenticate(ctx, request, true)
	if err != nil {
		return nil, err
	}
	return authorization.RelayPolicy, nil
}

func (a *TicketAuthenticator) authenticate(ctx context.Context, request protocol.PublicDirectAuthRequest, requirePolicy bool) (Authorization, error) {
	if a == nil || len(a.publicKey) != ed25519.PublicKeySize {
		return Authorization{}, ErrAuthenticatorRequired
	}
	request.ClientDeviceID = strings.TrimSpace(request.ClientDeviceID)
	request.ExitDeviceID = strings.TrimSpace(request.ExitDeviceID)
	if request.Version != protocol.PublicDirectAuthVersion ||
		request.ClientDeviceID == "" || request.ExitDeviceID != a.exitID ||
		len(request.Ticket) == 0 || len(request.Ticket) > maxSignedTicketSize {
		return Authorization{}, ErrUnauthorized
	}

	var signed protocol.PublicDirectSignedTicket
	if err := json.Unmarshal(request.Ticket, &signed); err != nil {
		return Authorization{}, ErrUnauthorized
	}
	claims := signed.Claims
	if claims.Version != protocol.PublicDirectTicketVersion ||
		strings.TrimSpace(claims.Issuer) != a.issuer ||
		strings.TrimSpace(claims.ClientDeviceID) != request.ClientDeviceID ||
		strings.TrimSpace(claims.ExitDeviceID) != request.ExitDeviceID ||
		claims.ExitDeviceID != a.exitID ||
		claims.PolicyRevision <= 0 || claims.AuthorizationRevision <= 0 ||
		len(claims.Nonce) < 16 || len(claims.Nonce) > 64 ||
		!containsDirectCapability(claims.AllowedCapabilities, protocol.PublicDirectTicketCapabilityProxy) {
		return Authorization{}, ErrUnauthorized
	}
	if len(signed.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(a.publicKey, protocol.PublicDirectTicketPayload(claims), signed.Signature) {
		return Authorization{}, ErrUnauthorized
	}

	now := a.now().UTC()
	if claims.IssuedAt > now.Add(maxTicketClockSkew).Unix() ||
		claims.ExpiresAt <= claims.IssuedAt {
		return Authorization{}, ErrUnauthorized
	}
	if claims.ExpiresAt <= now.Unix() {
		return Authorization{}, ErrTicketExpired
	}
	if time.Duration(claims.ExpiresAt-claims.IssuedAt)*time.Second > maxTicketLifetime ||
		claims.IssuedAt < now.Add(-maxTicketLifetime-maxTicketClockSkew).Unix() {
		return Authorization{}, ErrUnauthorized
	}

	a.mu.Lock()
	currentValidator := a.currentValidator
	a.mu.Unlock()
	authorization := Authorization{}
	if currentValidator != nil {
		current, err := currentValidator(ctx, claims)
		if err != nil {
			return Authorization{}, ErrUnauthorized
		}
		relayPolicy, err := normalizeDirectRelayPolicy(current.RelayPolicy)
		if err != nil {
			return Authorization{}, ErrUnauthorized
		}
		authorization = Authorization{
			RelayPolicy: relayPolicy,
			BrutalUploadBPS: current.BrutalUploadBPS,
			BrutalDownloadBPS: current.BrutalDownloadBPS,
		}
	} else if requirePolicy {
		return Authorization{}, ErrUnauthorized
	}

	replayKey := claims.Issuer + "\x00" + string(claims.Nonce)
	a.mu.Lock()
	defer a.mu.Unlock()
	nowUnix := now.Unix()
	for key, expiresAt := range a.replay {
		if expiresAt <= nowUnix {
			delete(a.replay, key)
		}
	}
	if _, exists := a.replay[replayKey]; exists {
		return Authorization{}, ErrTicketReplay
	}
	if len(a.replay) >= maxReplayEntries {
		return Authorization{}, ErrUnauthorized
	}
	a.replay[replayKey] = claims.ExpiresAt
	return authorization, nil
}

func normalizeDirectRelayPolicy(policy *acl.Policy) (*acl.Policy, error) {
	if policy == nil || strings.TrimSpace(policy.Fingerprint) == "" {
		return nil, errors.New("public direct relay ACL is required")
	}
	checker, err := acl.NewChecker(*policy)
	if err != nil {
		return nil, err
	}
	normalized := checker.Policy()
	if normalized.Fingerprint != policy.Fingerprint {
		return nil, errors.New("public direct relay ACL fingerprint mismatch")
	}
	return &normalized, nil
}

func containsDirectCapability(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
