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
)

type registrationStream struct {
	read  bytes.Buffer
	write bytes.Buffer
}

func (s *registrationStream) Read(p []byte) (int, error)       { return s.read.Read(p) }
func (s *registrationStream) Write(p []byte) (int, error)      { return s.write.Write(p) }
func (s *registrationStream) Close() error                     { return nil }
func (s *registrationStream) CloseWrite() error                { return nil }
func (s *registrationStream) SetDeadline(time.Time) error      { return nil }
func (s *registrationStream) SetReadDeadline(time.Time) error  { return nil }
func (s *registrationStream) SetWriteDeadline(time.Time) error { return nil }

type registrationSession struct {
	stream *registrationStream
	done   chan struct{}
}

func (s *registrationSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	if s.stream == nil {
		return nil, errors.New("missing stream")
	}
	return s.stream, nil
}
func (s *registrationSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}
func (s *registrationSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *registrationSession) RemoteAddr() net.Addr            { return &net.TCPAddr{} }
func (s *registrationSession) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (s *registrationSession) Close() error                    { return nil }
func (s *registrationSession) Done() <-chan struct{}           { return s.done }

func TestRegisterEndpointUsesPublicDirectControlFrame(t *testing.T) {
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectRegistrationResponse{
		Success: true,
		Endpoints: []protocol.PublicDirectEndpoint{{
			Protocol: protocol.PublicDirectEndpointProtocolUDP,
			Address:  "203.0.113.20:35820",
			Source:   protocol.PublicDirectEndpointManual,
			Verified: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	session := &registrationSession{stream: stream, done: make(chan struct{})}
	request := protocol.PublicDirectRegistrationRequest{
		ListenerPort:    35820,
		CertFingerprint: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		NetworkEpoch:    3,
	}
	response, err := RegisterEndpoint(context.Background(), session, request)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Success || len(response.Endpoints) != 1 {
		t.Fatalf("response=%+v", response)
	}

	header, err := protocol.ReadStreamHeader(&stream.write)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != protocol.FrameTypePublicDirectControl {
		t.Fatalf("frame type=%d", header.Type)
	}
	var sent protocol.PublicDirectRegistrationRequest
	if err := protocol.ReadJSON(&stream.write, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Operation != protocol.PublicDirectControlRegister ||
		sent.ListenerPort != request.ListenerPort || sent.NetworkEpoch != request.NetworkEpoch ||
		sent.CertFingerprint != request.CertFingerprint {
		t.Fatalf("sent request=%+v", sent)
	}
}

func TestValidateTicketCurrentUsesPublicDirectControlFrame(t *testing.T) {
	stream := &registrationStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectRegistrationResponse{
		Success: true, RelayPolicy: testDirectRelayPolicy(t),
	}); err != nil {
		t.Fatal(err)
	}
	session := &registrationSession{stream: stream, done: make(chan struct{})}
	claims := protocol.PublicDirectTicketClaims{
		ClientDeviceID:        "client",
		ExitDeviceID:          "exit",
		PolicyRevision:        4,
		AuthorizationRevision: 9,
	}
	policy, err := ValidateTicketCurrent(context.Background(), session, claims)
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil || policy.Fingerprint == "" {
		t.Fatalf("relay policy=%+v", policy)
	}

	header, err := protocol.ReadStreamHeader(&stream.write)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != protocol.FrameTypePublicDirectControl || header.RequestID != "public_direct_validate_ticket" {
		t.Fatalf("header=%+v", header)
	}
	var sent protocol.PublicDirectRegistrationRequest
	if err := protocol.ReadJSON(&stream.write, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Operation != protocol.PublicDirectControlValidateTicket || sent.TicketValidation == nil {
		t.Fatalf("sent request=%+v", sent)
	}
	if sent.TicketValidation.ClientDeviceID != claims.ClientDeviceID ||
		sent.TicketValidation.ExitDeviceID != claims.ExitDeviceID ||
		sent.TicketValidation.PolicyRevision != claims.PolicyRevision ||
		sent.TicketValidation.AuthorizationRevision != claims.AuthorizationRevision {
		t.Fatalf("ticket validation=%+v", sent.TicketValidation)
	}
}
