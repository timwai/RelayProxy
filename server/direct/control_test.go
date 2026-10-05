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
