package direct

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"relayproxy/internal/protocol"
)

const (
	defaultTicketTTL = 5 * time.Minute
	maxTicketTTL     = 5 * time.Minute
	ticketNonceSize  = 32
)

type TicketIssue struct {
	ClientDeviceID        string
	ExitDeviceID          string
	PolicyRevision        int64
	AuthorizationRevision int64
}

type TicketAuthority struct {
	issuer  string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	ttl     time.Duration
	now     func() time.Time
}

func NewTicketAuthority(issuer string) (*TicketAuthority, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, errors.New("public direct ticket issuer is required")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &TicketAuthority{
		issuer: issuer, public: publicKey, private: privateKey,
		ttl: defaultTicketTTL, now: time.Now,
	}, nil
}

func (a *TicketAuthority) Issuer() string {
	if a == nil {
		return ""
	}
	return a.issuer
}

func (a *TicketAuthority) PublicKey() []byte {
	if a == nil {
		return nil
	}
	return append([]byte(nil), a.public...)
}

func (a *TicketAuthority) Issue(issue TicketIssue) ([]byte, int64, error) {
	if a == nil || len(a.private) != ed25519.PrivateKeySize {
		return nil, 0, errors.New("public direct ticket authority is unavailable")
	}
	issue.ClientDeviceID = strings.TrimSpace(issue.ClientDeviceID)
	issue.ExitDeviceID = strings.TrimSpace(issue.ExitDeviceID)
	if issue.ClientDeviceID == "" || issue.ExitDeviceID == "" || issue.ClientDeviceID == issue.ExitDeviceID {
		return nil, 0, errors.New("public direct ticket requires distinct client and exit device ids")
	}
	if issue.PolicyRevision <= 0 || issue.AuthorizationRevision <= 0 {
		return nil, 0, errors.New("public direct ticket requires current policy and authorization revisions")
	}
	ttl := a.ttl
	if ttl <= 0 || ttl > maxTicketTTL {
		ttl = maxTicketTTL
	}
	now := a.now().UTC()
	nonce := make([]byte, ticketNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, 0, err
	}
	claims := protocol.PublicDirectTicketClaims{
		Version:               protocol.PublicDirectTicketVersion,
		Issuer:                a.issuer,
		ClientDeviceID:        issue.ClientDeviceID,
		ExitDeviceID:          issue.ExitDeviceID,
		IssuedAt:              now.Unix(),
		ExpiresAt:             now.Add(ttl).Unix(),
		PolicyRevision:        issue.PolicyRevision,
		AuthorizationRevision: issue.AuthorizationRevision,
		Nonce:                 nonce,
		AllowedCapabilities:   []string{protocol.PublicDirectTicketCapabilityProxy},
	}
	signed := protocol.PublicDirectSignedTicket{
		Claims:    claims,
		Signature: ed25519.Sign(a.private, protocol.PublicDirectTicketPayload(claims)),
	}
	raw, err := json.Marshal(signed)
	if err != nil {
		return nil, 0, err
	}
	return raw, claims.ExpiresAt, nil
}
