package direct

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
)

func signedTicketForTest(t *testing.T, privateKey ed25519.PrivateKey, claims protocol.PublicDirectTicketClaims) []byte {
	t.Helper()
	signed := protocol.PublicDirectSignedTicket{
		Claims:    claims,
		Signature: ed25519.Sign(privateKey, protocol.PublicDirectTicketPayload(claims)),
	}
	raw, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testDirectRelayPolicy(t *testing.T) *acl.Policy {
	t.Helper()
	checker, err := acl.NewChecker(acl.Policy{
		ID: "relay-test", AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := checker.Policy()
	return &policy
}

func validTicketClaims(now time.Time) protocol.PublicDirectTicketClaims {
	return protocol.PublicDirectTicketClaims{
		Version:               protocol.PublicDirectTicketVersion,
		Issuer:                "server-1",
		ClientDeviceID:        "client",
		ExitDeviceID:          "exit",
		IssuedAt:              now.Unix(),
		ExpiresAt:             now.Add(5 * time.Minute).Unix(),
		PolicyRevision:        4,
		AuthorizationRevision: 9,
		Nonce:                 []byte("0123456789abcdef0123456789abcdef"),
		AllowedCapabilities:   []string{protocol.PublicDirectTicketCapabilityProxy},
	}
}

func TestTicketAuthenticatorAcceptsOnceAndRejectsReplay(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	auth, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now }
	ticket := signedTicketForTest(t, privateKey, validTicketClaims(now))
	request := protocol.PublicDirectAuthRequest{
		Version:        protocol.PublicDirectAuthVersion,
		ClientDeviceID: "client",
		ExitDeviceID:   "exit",
		Ticket:         ticket,
	}
	if err := auth.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(context.Background(), request); !errors.Is(err, ErrTicketReplay) {
		t.Fatalf("replay error=%v", err)
	}
}

func TestTicketAuthenticatorRejectsExpiredTamperedAndWrongScope(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	newAuth := func() *TicketAuthenticator {
		auth, err := NewTicketAuthenticator("server-1", publicKey, "exit")
		if err != nil {
			t.Fatal(err)
		}
		auth.now = func() time.Time { return now }
		return auth
	}

	expiredClaims := validTicketClaims(now.Add(-10 * time.Minute))
	expiredClaims.ExpiresAt = now.Add(-time.Minute).Unix()
	expired := signedTicketForTest(t, privateKey, expiredClaims)
	if err := newAuth().Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: expired,
	}); !errors.Is(err, ErrTicketExpired) {
		t.Fatalf("expired ticket error=%v", err)
	}

	valid := signedTicketForTest(t, privateKey, validTicketClaims(now))
	var signed protocol.PublicDirectSignedTicket
	if err := json.Unmarshal(valid, &signed); err != nil {
		t.Fatal(err)
	}
	signed.Claims.PolicyRevision++
	tampered, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := newAuth().Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: tampered,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("tampered ticket error=%v", err)
	}

	if err := newAuth().Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "other-client", ExitDeviceID: "exit", Ticket: valid,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong-client ticket error=%v", err)
	}
	if err := newAuth().Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "other-exit", Ticket: valid,
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong-exit ticket error=%v", err)
	}
}

func TestTicketAuthenticatorRejectsFutureAndOverlongTickets(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	auth, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now }

	future := validTicketClaims(now.Add(time.Minute))
	future.ExpiresAt = future.IssuedAt + int64((5*time.Minute)/time.Second)
	if err := auth.Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit",
		Ticket: signedTicketForTest(t, privateKey, future),
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("future ticket error=%v", err)
	}

	overlong := validTicketClaims(now)
	overlong.ExpiresAt = overlong.IssuedAt + int64((maxTicketLifetime+time.Second)/time.Second)
	if err := auth.Authenticate(context.Background(), protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit",
		Ticket: signedTicketForTest(t, privateKey, overlong),
	}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("overlong ticket error=%v", err)
	}
}

func TestTicketAuthenticatorRequiresCurrentAuthorizationRevision(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	claims := validTicketClaims(now)
	request := protocol.PublicDirectAuthRequest{
		Version:        protocol.PublicDirectAuthVersion,
		ClientDeviceID: claims.ClientDeviceID,
		ExitDeviceID:   claims.ExitDeviceID,
		Ticket:         signedTicketForTest(t, privateKey, claims),
	}

	current, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	current.now = func() time.Time { return now }
	var validated protocol.PublicDirectTicketClaims
	current.SetCurrentValidator(func(_ context.Context, got protocol.PublicDirectTicketClaims) (Authorization, error) {
		validated = got
		return Authorization{
			RelayPolicy: testDirectRelayPolicy(t),
			BrutalUploadBPS: 12_500_000, BrutalDownloadBPS: 50_000_000,
		}, nil
	})
	if err := current.Authenticate(context.Background(), request); err != nil {
		t.Fatalf("current ticket rejected: %v", err)
	}
	if validated.PolicyRevision != claims.PolicyRevision ||
		validated.AuthorizationRevision != claims.AuthorizationRevision {
		t.Fatalf("validated claims=%+v", validated)
	}

	stale, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	stale.now = func() time.Time { return now }
	stale.SetCurrentValidator(func(context.Context, protocol.PublicDirectTicketClaims) (Authorization, error) {
		return Authorization{}, errors.New("authorization revoked")
	})
	if err := stale.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale ticket error=%v", err)
	}
}

func TestTicketAuthenticatorPolicyAuthenticationFailsClosedWithoutCurrentPolicy(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	claims := validTicketClaims(now)
	request := protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: claims.ClientDeviceID,
		ExitDeviceID: claims.ExitDeviceID, Ticket: signedTicketForTest(t, privateKey, claims),
	}

	auth, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now }
	if policy, err := auth.AuthenticatePolicy(context.Background(), request); !errors.Is(err, ErrUnauthorized) || policy != nil {
		t.Fatalf("missing current policy authentication policy=%+v err=%v", policy, err)
	}

	auth.SetCurrentValidator(func(context.Context, protocol.PublicDirectTicketClaims) (Authorization, error) {
		return Authorization{RelayPolicy: testDirectRelayPolicy(t)}, nil
	})
	policy, err := auth.AuthenticatePolicy(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil || policy.Fingerprint == "" {
		t.Fatalf("validated relay policy=%+v", policy)
	}
}

func TestTicketAuthenticatorRejectsTamperedRelayPolicy(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	claims := validTicketClaims(now)
	request := protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: claims.ClientDeviceID,
		ExitDeviceID: claims.ExitDeviceID, Ticket: signedTicketForTest(t, privateKey, claims),
	}
	auth, err := NewTicketAuthenticator("server-1", publicKey, "exit")
	if err != nil {
		t.Fatal(err)
	}
	auth.now = func() time.Time { return now }
	auth.SetCurrentValidator(func(context.Context, protocol.PublicDirectTicketClaims) (Authorization, error) {
		policy := testDirectRelayPolicy(t)
		policy.Fingerprint = "tampered"
		return Authorization{RelayPolicy: policy}, nil
	})
	if policy, err := auth.AuthenticatePolicy(context.Background(), request); !errors.Is(err, ErrUnauthorized) || policy != nil {
		t.Fatalf("tampered relay policy policy=%+v err=%v", policy, err)
	}
}
