package direct

import (
	"context"
	"errors"
	"testing"
	"time"

	internaldirect "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
)

func TestTicketAuthenticatorVerifiesScopeRevisionAndReplay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := signer.Issue("client", "exit", 7, 11, []string{internaldirect.AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	auth := &TicketAuthenticator{
		VerifyKey:      signer.PublicKey(),
		ExitDeviceID:   "exit",
		PolicyRevision: 7,
		AuthorizationRevision: func(clientDeviceID string) (int64, bool) {
			return 11, clientDeviceID == "client"
		},
		Now: func() time.Time { return time.Now().UTC() },
	}
	request := protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: raw,
	}
	if err := auth.Authenticate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("replayed ticket error=%v", err)
	}
	_ = now
}

func TestTicketAuthenticatorRejectsWrongPolicyAndAuthorizationRevision(t *testing.T) {
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := signer.Issue("client", "exit", 7, 11, []string{internaldirect.AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.PublicDirectAuthRequest{
		Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: raw,
	}

	policy := &TicketAuthenticator{
		VerifyKey: signer.PublicKey(), ExitDeviceID: "exit", PolicyRevision: 8,
	}
	if err := policy.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong policy revision error=%v", err)
	}

	authorization := &TicketAuthenticator{
		VerifyKey: signer.PublicKey(), ExitDeviceID: "exit", PolicyRevision: 7,
		AuthorizationRevision: func(string) (int64, bool) { return 12, true },
	}
	if err := authorization.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong authorization revision error=%v", err)
	}
}

func TestTicketAuthenticatorRejectsWrongClientExitAndSignature(t *testing.T) {
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := signer.Issue("client", "exit", 7, 0, []string{internaldirect.AccessCapabilityProxy})
	if err != nil {
		t.Fatal(err)
	}
	auth := &TicketAuthenticator{
		VerifyKey: signer.PublicKey(), ExitDeviceID: "exit", PolicyRevision: 7,
	}
	for _, request := range []protocol.PublicDirectAuthRequest{
		{Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "other", ExitDeviceID: "exit", Ticket: raw},
		{Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "other", Ticket: raw},
		{Version: protocol.PublicDirectAuthVersion, ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: append(append([]byte(nil), raw...), 0)},
	} {
		if err := auth.Authenticate(context.Background(), request); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("request=%+v error=%v", request, err)
		}
	}
}
