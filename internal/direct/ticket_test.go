package direct

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testTicketSigner(t *testing.T, now time.Time) *TicketSigner {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewTicketSigner(privateKey, DefaultAccessTicketTTL)
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return now }
	return signer
}

func TestAccessTicketRoundTripBindsAuthorizationContext(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer := testTicketSigner(t, now)
	raw, issued, err := signer.Issue("client-1", "exit-1", 7, 12, []string{AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	if issued.ExpiresAt-issued.IssuedAt != int64(DefaultAccessTicketTTL/time.Second) {
		t.Fatalf("ticket lifetime=%ds", issued.ExpiresAt-issued.IssuedAt)
	}
	claims, err := VerifyAccessTicket(signer.PublicKey(), raw, TicketVerifyOptions{
		ClientDeviceID: "client-1", ExitDeviceID: "exit-1",
		PolicyRevision: 7, AuthorizationRevision: 12,
		RequiredCapability: AccessCapabilityProxy,
		Now:                now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if claims.ClientDeviceID != "client-1" || claims.ExitDeviceID != "exit-1" ||
		claims.PolicyRevision != 7 || claims.AuthorizationRevision != 12 ||
		len(claims.Nonce) != 32 {
		t.Fatalf("claims=%+v", claims)
	}
}

func TestAccessTicketRejectsTampering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer := testTicketSigner(t, now)
	raw, _, err := signer.Issue("client-1", "exit-1", 7, 12, []string{AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	var envelope SignedAccessTicket
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Claims.ExitDeviceID = "exit-2"
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAccessTicket(signer.PublicKey(), tampered, TicketVerifyOptions{
		ClientDeviceID: "client-1", ExitDeviceID: "exit-2",
		PolicyRevision: 7, AuthorizationRevision: 12,
		RequiredCapability: AccessCapabilityProxy, Now: now,
	}); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("tampered ticket error=%v", err)
	}
}

func TestAccessTicketRejectsExpiredAndFutureTickets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer := testTicketSigner(t, now)
	raw, _, err := signer.Issue("client-1", "exit-1", 7, 12, []string{AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	base := TicketVerifyOptions{
		ClientDeviceID: "client-1", ExitDeviceID: "exit-1",
		PolicyRevision: 7, AuthorizationRevision: 12,
		RequiredCapability: AccessCapabilityProxy,
	}
	expired := base
	expired.Now = now.Add(DefaultAccessTicketTTL)
	if _, err := VerifyAccessTicket(signer.PublicKey(), raw, expired); !errors.Is(err, ErrExpiredTicket) {
		t.Fatalf("expired ticket error=%v", err)
	}

	futureSigner := testTicketSigner(t, now.Add(2*time.Minute))
	futureRaw, _, err := futureSigner.Issue("client-1", "exit-1", 7, 12, []string{AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	future := base
	future.Now = now
	future.ClockSkew = 30 * time.Second
	if _, err := VerifyAccessTicket(futureSigner.PublicKey(), futureRaw, future); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("future ticket error=%v", err)
	}
}

func TestAccessTicketRejectsWrongScopeRevisionAndCapability(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer := testTicketSigner(t, now)
	raw, _, err := signer.Issue("client-1", "exit-1", 7, 12, []string{AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	base := TicketVerifyOptions{
		ClientDeviceID: "client-1", ExitDeviceID: "exit-1",
		PolicyRevision: 7, AuthorizationRevision: 12,
		RequiredCapability: AccessCapabilityProxy, Now: now,
	}

	tests := []struct {
		name string
		opts TicketVerifyOptions
		want error
	}{
		{name: "client", opts: func() TicketVerifyOptions { v := base; v.ClientDeviceID = "client-2"; return v }(), want: ErrTicketScope},
		{name: "exit", opts: func() TicketVerifyOptions { v := base; v.ExitDeviceID = "exit-2"; return v }(), want: ErrTicketScope},
		{name: "policy", opts: func() TicketVerifyOptions { v := base; v.PolicyRevision = 8; return v }(), want: ErrTicketRevision},
		{name: "authorization", opts: func() TicketVerifyOptions { v := base; v.AuthorizationRevision = 13; return v }(), want: ErrTicketRevision},
		{name: "capability", opts: func() TicketVerifyOptions { v := base; v.RequiredCapability = "udp-admin"; return v }(), want: ErrTicketCapability},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyAccessTicket(signer.PublicKey(), raw, tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
}

func TestTicketSignerRejectsLongTTL(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewTicketSigner(privateKey, DefaultAccessTicketTTL+time.Second); err == nil {
		t.Fatal("signer accepted ticket ttl above five minutes")
	}
}
