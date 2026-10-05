package direct

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestTicketAuthorityIssuesScopedSignedTicket(t *testing.T) {
	authority, err := NewTicketAuthority("server-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	authority.now = func() time.Time { return now }
	authority.ttl = 10 * time.Minute // must still be capped at the V1 maximum.

	raw, expiresAt, err := authority.Issue(TicketIssue{
		ClientDeviceID:        "client",
		ExitDeviceID:          "exit",
		PolicyRevision:        7,
		AuthorizationRevision: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	var signed protocol.PublicDirectSignedTicket
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatal(err)
	}
	claims := signed.Claims
	if claims.Version != protocol.PublicDirectTicketVersion ||
		claims.Issuer != "server-1" || claims.ClientDeviceID != "client" ||
		claims.ExitDeviceID != "exit" || claims.PolicyRevision != 7 ||
		claims.AuthorizationRevision != 11 {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.IssuedAt != now.Unix() || claims.ExpiresAt != now.Add(maxTicketTTL).Unix() ||
		expiresAt != claims.ExpiresAt {
		t.Fatalf("ticket lifetime issued=%d expires=%d return=%d", claims.IssuedAt, claims.ExpiresAt, expiresAt)
	}
	if len(claims.Nonce) != ticketNonceSize {
		t.Fatalf("nonce length=%d", len(claims.Nonce))
	}
	if len(claims.AllowedCapabilities) != 1 ||
		claims.AllowedCapabilities[0] != protocol.PublicDirectTicketCapabilityProxy {
		t.Fatalf("capabilities=%v", claims.AllowedCapabilities)
	}
	if !ed25519.Verify(authority.PublicKey(), protocol.PublicDirectTicketPayload(claims), signed.Signature) {
		t.Fatal("ticket signature did not verify")
	}
}

func TestTicketAuthorityRejectsUnrevisionedAuthorization(t *testing.T) {
	authority, err := NewTicketAuthority("server-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range []TicketIssue{
		{ClientDeviceID: "client", ExitDeviceID: "exit", PolicyRevision: 0, AuthorizationRevision: 1},
		{ClientDeviceID: "client", ExitDeviceID: "exit", PolicyRevision: 1, AuthorizationRevision: 0},
		{ClientDeviceID: "same", ExitDeviceID: "same", PolicyRevision: 1, AuthorizationRevision: 1},
	} {
		if _, _, err := authority.Issue(issue); err == nil {
			t.Fatalf("invalid issue accepted: %+v", issue)
		}
	}
}

func TestTicketAuthorityUsesUniqueNonce(t *testing.T) {
	authority, err := NewTicketAuthority("server-1")
	if err != nil {
		t.Fatal(err)
	}
	issue := TicketIssue{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 1, AuthorizationRevision: 1,
	}
	firstRaw, _, err := authority.Issue(issue)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, _, err := authority.Issue(issue)
	if err != nil {
		t.Fatal(err)
	}
	var first, second protocol.PublicDirectSignedTicket
	if err := json.Unmarshal(firstRaw, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatal(err)
	}
	if string(first.Claims.Nonce) == string(second.Claims.Nonce) {
		t.Fatal("two tickets reused the same nonce")
	}
}
