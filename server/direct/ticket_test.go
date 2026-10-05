package direct

import (
	"context"
	"errors"
	"testing"
	"time"

	internaldirect "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/server/session"
)

func verifiedTicketRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	now := time.Now()
	registry.records["exit"] = map[string]EndpointRecord{
		"203.0.113.20:35820": {
			DeviceID: "exit", SessionID: "exit-session", NetworkEpoch: 1,
			CertFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			Endpoint: protocol.PublicDirectEndpoint{
				Protocol: protocol.PublicDirectEndpointProtocolUDP,
				Address:  "203.0.113.20:35820",
				Source:   protocol.PublicDirectEndpointManual,
				Verified: true,
			},
			State: StateVerified, RegisteredAt: now, VerifiedAt: now, ExpiresAt: now.Add(time.Minute),
		},
	}
	return registry
}

func TestTicketIssuerSignsCurrentAuthorizedContext(t *testing.T) {
	registry := verifiedTicketRegistry(t)
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &TicketIssuer{
		Registry: registry,
		Signer:   signer,
		Authorize: func(clientDeviceID, exitDeviceID string) (TicketAuthorizationContext, bool, error) {
			if clientDeviceID != "client" || exitDeviceID != "exit" {
				return TicketAuthorizationContext{}, false, nil
			}
			return TicketAuthorizationContext{PolicyRevision: 7, AuthorizationRevision: 11}, true, nil
		},
		Sync: func(_ context.Context, exitDeviceID string, update protocol.PublicDirectAuthorizationUpdate) error {
			if exitDeviceID != "exit" || update.ClientDeviceID != "client" ||
				update.PolicyRevision != 7 || update.AuthorizationRevision != 11 || !update.Authorized {
				t.Fatalf("unexpected authorization sync: exit=%s update=%+v", exitDeviceID, update)
			}
			return nil
		},
	}
	stream := newControlStream(t, protocol.PublicDirectTicketRequest{ExitDeviceID: "exit"})
	issuer.HandleControl(context.Background(), stream, &session.DeviceSession{
		DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
	})

	var response protocol.PublicDirectTicketResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || len(response.Ticket) == 0 ||
		response.PolicyRevision != 7 || response.AuthorizationRevision != 11 {
		t.Fatalf("ticket response=%+v", response)
	}
	if _, err := internaldirect.VerifyAccessTicket(signer.PublicKey(), response.Ticket, internaldirect.TicketVerifyOptions{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 7, AuthorizationRevision: 11,
		RequiredCapability: internaldirect.AccessCapabilityProxy,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTicketIssuerRejectsUnverifiedOrUnauthorizedExit(t *testing.T) {
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		registry  *Registry
		authorize TicketAuthorizeFunc
	}{
		{
			name:     "unverified",
			registry: NewRegistry(),
			authorize: func(string, string) (TicketAuthorizationContext, bool, error) {
				t.Fatal("authorization should not run for an unverified endpoint")
				return TicketAuthorizationContext{}, false, nil
			},
		},
		{
			name:     "unauthorized",
			registry: verifiedTicketRegistry(t),
			authorize: func(string, string) (TicketAuthorizationContext, bool, error) {
				return TicketAuthorizationContext{}, false, nil
			},
		},
		{
			name:     "authorization error",
			registry: verifiedTicketRegistry(t),
			authorize: func(string, string) (TicketAuthorizationContext, bool, error) {
				return TicketAuthorizationContext{}, false, errors.New("db failed")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer := &TicketIssuer{
				Registry: tc.registry, Signer: signer, Authorize: tc.authorize,
				Sync: func(context.Context, string, protocol.PublicDirectAuthorizationUpdate) error { return nil },
			}
			stream := newControlStream(t, protocol.PublicDirectTicketRequest{ExitDeviceID: "exit"})
			issuer.HandleControl(context.Background(), stream, &session.DeviceSession{
				DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
			})
			var response protocol.PublicDirectTicketResponse
			if err := protocol.ReadJSON(&stream.write, &response); err != nil {
				t.Fatal(err)
			}
			if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied || len(response.Ticket) != 0 {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

func TestTicketIssuerRejectsAuthorizationSyncFailure(t *testing.T) {
	signer, err := internaldirect.GenerateTicketSigner(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &TicketIssuer{
		Registry: verifiedTicketRegistry(t),
		Signer:   signer,
		Authorize: func(string, string) (TicketAuthorizationContext, bool, error) {
			return TicketAuthorizationContext{PolicyRevision: 7, AuthorizationRevision: 11}, true, nil
		},
		Sync: func(context.Context, string, protocol.PublicDirectAuthorizationUpdate) error {
			return errors.New("exit sync failed")
		},
	}
	stream := newControlStream(t, protocol.PublicDirectTicketRequest{ExitDeviceID: "exit"})
	issuer.HandleControl(context.Background(), stream, &session.DeviceSession{
		DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
	})
	var response protocol.PublicDirectTicketResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied || len(response.Ticket) != 0 {
		t.Fatalf("response=%+v", response)
	}
}

func TestTicketIssuerRejectsNonClientSession(t *testing.T) {
	issuer := &TicketIssuer{Registry: verifiedTicketRegistry(t)}
	stream := newControlStream(t, protocol.PublicDirectTicketRequest{ExitDeviceID: "exit"})
	issuer.HandleControl(context.Background(), stream, &session.DeviceSession{
		DeviceID: "exit", Grants: []string{protocol.CapabilityProxyExit},
	})
	var response protocol.PublicDirectTicketResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied {
		t.Fatalf("response=%+v", response)
	}
}
