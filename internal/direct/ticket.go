package direct

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	AccessTicketVersion   = 1
	AccessCapabilityProxy = "proxy"
)

const (
	DefaultAccessTicketTTL = 5 * time.Minute
	maxAccessTicketTTL     = 5 * time.Minute
	defaultClockSkew       = 30 * time.Second
)

var (
	ErrInvalidTicket    = errors.New("invalid public direct access ticket")
	ErrExpiredTicket    = errors.New("public direct access ticket expired")
	ErrTicketScope      = errors.New("public direct access ticket scope mismatch")
	ErrTicketRevision   = errors.New("public direct access ticket revision mismatch")
	ErrTicketCapability = errors.New("public direct access ticket capability missing")
)

type AccessTicketClaims struct {
	Version               int      `json:"version"`
	ClientDeviceID        string   `json:"clientDeviceId"`
	ExitDeviceID          string   `json:"exitDeviceId"`
	IssuedAt              int64    `json:"issuedAt"`
	ExpiresAt             int64    `json:"expiresAt"`
	PolicyRevision        int64    `json:"policyRevision"`
	AuthorizationRevision int64    `json:"authorizationRevision"`
	Nonce                 []byte   `json:"nonce"`
	AllowedCapabilities   []string `json:"allowedCapabilities"`
}

type SignedAccessTicket struct {
	Claims    AccessTicketClaims `json:"claims"`
	Signature []byte             `json:"signature"`
}

type TicketSigner struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	ttl        time.Duration
	now        func() time.Time
}

func GenerateTicketSigner(ttl time.Duration) (*TicketSigner, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return NewTicketSigner(privateKey, ttl)
}

func NewTicketSigner(privateKey ed25519.PrivateKey, ttl time.Duration) (*TicketSigner, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("public direct ticket signer requires an Ed25519 private key")
	}
	if ttl <= 0 {
		ttl = DefaultAccessTicketTTL
	}
	if ttl > maxAccessTicketTTL {
		return nil, fmt.Errorf("public direct ticket ttl %s exceeds maximum %s", ttl, maxAccessTicketTTL)
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("public direct ticket signer has invalid Ed25519 public key")
	}
	return &TicketSigner{
		privateKey: append(ed25519.PrivateKey(nil), privateKey...),
		publicKey:  append(ed25519.PublicKey(nil), publicKey...),
		ttl:        ttl,
		now:        time.Now,
	}, nil
}

func (s *TicketSigner) PublicKey() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.publicKey...)
}

func (s *TicketSigner) Issue(
	clientDeviceID string,
	exitDeviceID string,
	policyRevision int64,
	authorizationRevision int64,
	capabilities []string,
) ([]byte, AccessTicketClaims, error) {
	if s == nil || len(s.privateKey) != ed25519.PrivateKeySize {
		return nil, AccessTicketClaims{}, errors.New("public direct ticket signer is unavailable")
	}
	clientDeviceID = strings.TrimSpace(clientDeviceID)
	exitDeviceID = strings.TrimSpace(exitDeviceID)
	if clientDeviceID == "" || exitDeviceID == "" || clientDeviceID == exitDeviceID {
		return nil, AccessTicketClaims{}, errors.New("public direct ticket requires distinct client and exit device ids")
	}
	if policyRevision < 0 || authorizationRevision < 0 {
		return nil, AccessTicketClaims{}, errors.New("public direct ticket revisions must be non-negative")
	}
	capabilities = normalizeCapabilities(capabilities)
	if len(capabilities) == 0 {
		return nil, AccessTicketClaims{}, errors.New("public direct ticket requires at least one capability")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, AccessTicketClaims{}, err
	}
	now := s.now().UTC()
	claims := AccessTicketClaims{
		Version:               AccessTicketVersion,
		ClientDeviceID:        clientDeviceID,
		ExitDeviceID:          exitDeviceID,
		IssuedAt:              now.Unix(),
		ExpiresAt:             now.Add(s.ttl).Unix(),
		PolicyRevision:        policyRevision,
		AuthorizationRevision: authorizationRevision,
		Nonce:                 nonce,
		AllowedCapabilities:   capabilities,
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return nil, AccessTicketClaims{}, err
	}
	envelope := SignedAccessTicket{
		Claims:    claims,
		Signature: ed25519.Sign(s.privateKey, payload),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, AccessTicketClaims{}, err
	}
	return raw, claims, nil
}

type TicketVerifyOptions struct {
	ClientDeviceID        string
	ExitDeviceID          string
	PolicyRevision        int64
	AuthorizationRevision int64
	RequiredCapability    string
	Now                   time.Time
	ClockSkew             time.Duration
}

func VerifyAccessTicket(publicKey []byte, raw []byte, options TicketVerifyOptions) (AccessTicketClaims, error) {
	if len(publicKey) != ed25519.PublicKeySize || len(raw) == 0 {
		return AccessTicketClaims{}, ErrInvalidTicket
	}
	var envelope SignedAccessTicket
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return AccessTicketClaims{}, fmt.Errorf("%w: malformed envelope", ErrInvalidTicket)
	}
	if len(envelope.Signature) != ed25519.SignatureSize {
		return AccessTicketClaims{}, fmt.Errorf("%w: malformed signature", ErrInvalidTicket)
	}
	claims := envelope.Claims
	payload, err := json.Marshal(claims)
	if err != nil {
		return AccessTicketClaims{}, fmt.Errorf("%w: claims encoding", ErrInvalidTicket)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, envelope.Signature) {
		return AccessTicketClaims{}, fmt.Errorf("%w: signature", ErrInvalidTicket)
	}
	if claims.Version != AccessTicketVersion ||
		strings.TrimSpace(claims.ClientDeviceID) == "" ||
		strings.TrimSpace(claims.ExitDeviceID) == "" ||
		claims.ClientDeviceID == claims.ExitDeviceID ||
		len(claims.Nonce) < 16 ||
		claims.IssuedAt <= 0 ||
		claims.ExpiresAt <= claims.IssuedAt ||
		time.Duration(claims.ExpiresAt-claims.IssuedAt)*time.Second > maxAccessTicketTTL {
		return AccessTicketClaims{}, fmt.Errorf("%w: invalid claims", ErrInvalidTicket)
	}

	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	skew := options.ClockSkew
	if skew <= 0 {
		skew = defaultClockSkew
	}
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()
	if issuedAt.After(now.Add(skew)) {
		return AccessTicketClaims{}, fmt.Errorf("%w: issued in the future", ErrInvalidTicket)
	}
	if !expiresAt.After(now) {
		return AccessTicketClaims{}, ErrExpiredTicket
	}

	if expected := strings.TrimSpace(options.ClientDeviceID); expected != "" && claims.ClientDeviceID != expected {
		return AccessTicketClaims{}, fmt.Errorf("%w: client device", ErrTicketScope)
	}
	if expected := strings.TrimSpace(options.ExitDeviceID); expected != "" && claims.ExitDeviceID != expected {
		return AccessTicketClaims{}, fmt.Errorf("%w: exit device", ErrTicketScope)
	}
	if options.PolicyRevision >= 0 && claims.PolicyRevision != options.PolicyRevision {
		return AccessTicketClaims{}, fmt.Errorf("%w: policy", ErrTicketRevision)
	}
	if options.AuthorizationRevision >= 0 && claims.AuthorizationRevision != options.AuthorizationRevision {
		return AccessTicketClaims{}, fmt.Errorf("%w: authorization", ErrTicketRevision)
	}
	required := strings.TrimSpace(options.RequiredCapability)
	if required != "" && !containsCapability(claims.AllowedCapabilities, required) {
		return AccessTicketClaims{}, fmt.Errorf("%w: %s", ErrTicketCapability, required)
	}
	return claims, nil
}

func normalizeCapabilities(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsCapability(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
