package direct

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type testControlStream struct {
	read  bytes.Buffer
	write bytes.Buffer
}

func newControlStream(t *testing.T, request any) *testControlStream {
	t.Helper()
	stream := &testControlStream{}
	if err := protocol.WriteJSON(&stream.read, request); err != nil {
		t.Fatal(err)
	}
	return stream
}

func (s *testControlStream) Read(p []byte) (int, error)       { return s.read.Read(p) }
func (s *testControlStream) Write(p []byte) (int, error)      { return s.write.Write(p) }
func (s *testControlStream) Close() error                     { return nil }
func (s *testControlStream) CloseWrite() error                { return nil }
func (s *testControlStream) SetDeadline(time.Time) error      { return nil }
func (s *testControlStream) SetReadDeadline(time.Time) error  { return nil }
func (s *testControlStream) SetWriteDeadline(time.Time) error { return nil }

type observedSession struct {
	remote net.Addr
	done   chan struct{}
}

func (s *observedSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}
func (s *observedSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}
func (s *observedSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *observedSession) RemoteAddr() net.Addr            { return s.remote }
func (s *observedSession) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (s *observedSession) Close() error                    { return nil }
func (s *observedSession) Done() <-chan struct{}           { return s.done }

func TestControllerRegistersObservedEndpointForAuthenticatedExit(t *testing.T) {
	registry := NewRegistry()
	verifier := &Verifier{Registry: registry, Timeout: 20 * time.Millisecond}
	controller := NewController(context.Background(), registry, verifier, nil)
	defer controller.Close()

	stream := newControlStream(t, protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
		NetworkEpoch:    9,
	})
	dev := &session.DeviceSession{
		SessionID: "session-1",
		DeviceID:  "exit",
		Grants:    []string{protocol.CapabilityProxyExit},
		Tunnel: &observedSession{
			remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443},
			done:   make(chan struct{}),
		},
	}
	controller.HandleControl(context.Background(), stream, dev)

	var response protocol.PublicDirectRegistrationResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success {
		t.Fatalf("registration failed: %+v", response)
	}
	record, ok := registry.Lookup("exit", "session-1", "8.8.8.8:35820")
	if !ok || record.NetworkEpoch != 9 || record.Endpoint.Source != protocol.PublicDirectEndpointObserved {
		t.Fatalf("registered record=%+v ok=%v", record, ok)
	}
	if len(registry.VerifiedEndpoints("exit")) != 0 {
		t.Fatal("endpoint was published before verification")
	}
}

func TestControllerRejectsNonExitSession(t *testing.T) {
	registry := NewRegistry()
	controller := NewController(context.Background(), registry, &Verifier{Registry: registry}, nil)
	defer controller.Close()

	stream := newControlStream(t, protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: testFingerprint(),
	})
	controller.HandleControl(context.Background(), stream, &session.DeviceSession{
		SessionID: "session-1", DeviceID: "client",
		Grants: []string{protocol.CapabilityProxyClient},
	})

	var response protocol.PublicDirectRegistrationResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied {
		t.Fatalf("non-exit registration response=%+v", response)
	}
	if len(registry.Snapshot("client")) != 0 {
		t.Fatal("non-exit session created endpoint records")
	}
}


func TestControllerValidatesCurrentTicketRevision(t *testing.T) {
	for _, tc := range []struct {
		name        string
		validatorErr error
		wantSuccess bool
	}{
		{name: "current", wantSuccess: true},
		{name: "stale", validatorErr: errors.New("stale authorization")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := NewRegistry()
			controller := NewController(context.Background(), registry, &Verifier{Registry: registry}, nil)
			defer controller.Close()

			var gotExitID string
			var got protocol.PublicDirectTicketValidationRequest
			controller.SetTicketValidator(func(_ context.Context, exitID string, request protocol.PublicDirectTicketValidationRequest) error {
				gotExitID = exitID
				got = request
				return tc.validatorErr
			})

			request := protocol.PublicDirectRegistrationRequest{
				Operation: protocol.PublicDirectControlValidateTicket,
				TicketValidation: &protocol.PublicDirectTicketValidationRequest{
					ClientDeviceID:        "client",
					ExitDeviceID:          "exit",
					PolicyRevision:        4,
					AuthorizationRevision: 9,
				},
			}
			stream := newControlStream(t, request)
			dev := &session.DeviceSession{
				SessionID: "session-1",
				DeviceID:  "exit",
				Grants:    []string{protocol.CapabilityProxyExit},
				Tunnel: &observedSession{
					remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443},
					done:   make(chan struct{}),
				},
			}
			controller.HandleControl(context.Background(), stream, dev)

			var response protocol.PublicDirectRegistrationResponse
			if err := protocol.ReadJSON(&stream.write, &response); err != nil {
				t.Fatal(err)
			}
			if response.Success != tc.wantSuccess {
				t.Fatalf("response=%+v", response)
			}
			if !tc.wantSuccess && response.ErrorCode != protocol.ErrCodeAccessDenied {
				t.Fatalf("stale response=%+v", response)
			}
			if gotExitID != "exit" || got.ClientDeviceID != "client" || got.ExitDeviceID != "exit" ||
				got.PolicyRevision != 4 || got.AuthorizationRevision != 9 {
				t.Fatalf("validator args exit=%q request=%+v", gotExitID, got)
			}
		})
	}
}

func TestControllerRejectsTicketValidationForAnotherExit(t *testing.T) {
	registry := NewRegistry()
	controller := NewController(context.Background(), registry, &Verifier{Registry: registry}, nil)
	defer controller.Close()

	called := false
	controller.SetTicketValidator(func(context.Context, string, protocol.PublicDirectTicketValidationRequest) error {
		called = true
		return nil
	})
	stream := newControlStream(t, protocol.PublicDirectRegistrationRequest{
		Operation: protocol.PublicDirectControlValidateTicket,
		TicketValidation: &protocol.PublicDirectTicketValidationRequest{
			ClientDeviceID: "client", ExitDeviceID: "other-exit",
			PolicyRevision: 4, AuthorizationRevision: 9,
		},
	})
	controller.HandleControl(context.Background(), stream, &session.DeviceSession{
		SessionID: "session-1", DeviceID: "exit",
		Grants: []string{protocol.CapabilityProxyExit},
		Tunnel: &observedSession{remote: &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 443}, done: make(chan struct{})},
	})
	var response protocol.PublicDirectRegistrationResponse
	if err := protocol.ReadJSON(&stream.write, &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied || called {
		t.Fatalf("cross-exit validation response=%+v called=%v", response, called)
	}
}
