package browsersync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"sync"
	"time"
)

const challengeTTL = 45 * time.Second

var deviceIDPattern = regexp.MustCompile(`^browser_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Authenticate requires a fresh, single-use challenge and a registered,
// approved P-256 WebCrypto signing public key.
type Authenticator struct {
	store      *Store
	mu         sync.Mutex
	challenges map[string]challenge
}
type challenge struct {
	nonce    string
	deadline time.Time
}

func NewAuthenticator(store *Store) *Authenticator {
	return &Authenticator{store: store, challenges: map[string]challenge{}}
}
func Base64URL(v string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(v) }
func validateP256SPKI(encoded string) (*ecdsa.PublicKey, error) {
	bytes, err := Base64URL(encoded)
	if err != nil {
		return nil, err
	}
	if len(bytes) > 256 {
		return nil, errors.New("oversized public key")
	}
	key, err := x509.ParsePKIXPublicKey(bytes)
	if err != nil {
		return nil, err
	}
	k, ok := key.(*ecdsa.PublicKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, errors.New("expected P-256 signing key")
	}
	return k, nil
}
func ValidateRegistration(d BrowserDevice) error {
	if !deviceIDPattern.MatchString(d.ID) {
		return errors.New("invalid browser id")
	}
	if strings.TrimSpace(d.Name) == "" || len(d.Name) > 100 {
		return errors.New("invalid display name")
	}
	if _, err := validateP256SPKI(d.SigningKey); err != nil {
		return err
	}
	bytes, err := Base64URL(d.EncryptionKey)
	if err != nil || len(bytes) > 256 {
		return errors.New("invalid encryption key")
	}
	key, err := x509.ParsePKIXPublicKey(bytes)
	if err != nil {
		return err
	}
	ecdhKey, ok := key.(*ecdsa.PublicKey)
	if !ok || ecdhKey.Curve != elliptic.P256() {
		return errors.New("expected P-256 encryption public key")
	}
	return nil
}
func (a *Authenticator) Challenge(id string) (string, error) {
	if !deviceIDPattern.MatchString(id) {
		return "", errors.New("invalid browser id")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(buf)
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for key, c := range a.challenges {
		if !now.Before(c.deadline) {
			delete(a.challenges, key)
		}
	}
	if len(a.challenges) > 4096 {
		return "", errors.New("too many pending challenges")
	}
	a.challenges[id] = challenge{nonce: nonce, deadline: now.Add(challengeTTL)}
	return nonce, nil
}
func (a *Authenticator) Verify(ctx context.Context, id, origin, nonce, signature string) (BrowserDevice, error) {
	a.mu.Lock()
	stored, ok := a.challenges[id]
	delete(a.challenges, id) // consume before validation; prevents retries
	a.mu.Unlock()
	if !ok || stored.nonce != nonce || time.Now().After(stored.deadline) {
		return BrowserDevice{}, errors.New("expired or invalid challenge")
	}
	d, err := a.store.Device(ctx, id)
	if err != nil {
		return BrowserDevice{}, err
	}
	if d.State != "approved" || (!d.Send && !d.Receive) {
		return BrowserDevice{}, errors.New("browser not approved")
	}
	key, err := validateP256SPKI(d.SigningKey)
	if err != nil {
		return BrowserDevice{}, err
	}
	sig, err := Base64URL(signature)
	if err != nil || len(sig) != 64 {
		return BrowserDevice{}, errors.New("invalid signature length")
	}
	canonical := strings.Join([]string{"browser.sync.v1", origin, id, nonce}, "\n")
	digest := sha256Sum([]byte(canonical))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(key, digest[:], r, s) {
		return BrowserDevice{}, errors.New("signature verification failed")
	}
	return d, nil
}
