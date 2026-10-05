package direct

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type authSyncStream struct {
	read  bytes.Buffer
	write bytes.Buffer
}

func newAuthSyncStream(t *testing.T, success bool) *authSyncStream {
	t.Helper()
	stream := &authSyncStream{}
	if err := protocol.WriteJSON(&stream.read, protocol.PublicDirectAuthorizationReceipt{Success: success}); err != nil {
		t.Fatal(err)
	}
	return stream
}

func (s *authSyncStream) Read(p []byte) (int, error)       { return s.read.Read(p) }
func (s *authSyncStream) Write(p []byte) (int, error)      { return s.write.Write(p) }
func (s *authSyncStream) Close() error                     { return nil }
func (s *authSyncStream) CloseWrite() error                { return nil }
func (s *authSyncStream) SetDeadline(time.Time) error      { return nil }
func (s *authSyncStream) SetReadDeadline(time.Time) error  { return nil }
func (s *authSyncStream) SetWriteDeadline(time.Time) error { return nil }

type authSyncSession struct {
	stream  tunnel.TunnelStream
	openErr error
	closed  atomic.Bool
	done    chan struct{}
}

func (s *authSyncSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return s.stream, nil
}
func (s *authSyncSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}
func (s *authSyncSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *authSyncSession) RemoteAddr() net.Addr            { return &net.TCPAddr{} }
func (s *authSyncSession) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (s *authSyncSession) Close() error {
	if s.closed.CompareAndSwap(false, true) && s.done != nil {
		close(s.done)
	}
	return nil
}
func (s *authSyncSession) Done() <-chan struct{} { return s.done }

func TestAuthorizationSyncerPushesRevisionToCurrentExit(t *testing.T) {
	manager := session.NewManager()
	stream := newAuthSyncStream(t, true)
	transport := &authSyncSession{stream: stream, done: make(chan struct{})}
	manager.Register(&session.DeviceSession{
		DeviceID: "exit", PolicyRevision: 7,
		Grants:       []string{protocol.CapabilityProxyExit},
		Capabilities: []string{protocol.CapabilityProxyPublicDirect},
		Tunnel:       transport,
	})
	syncer := &AuthorizationSyncer{Sessions: manager}
	update := protocol.PublicDirectAuthorizationUpdate{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 7, AuthorizationRevision: 11, Authorized: true,
	}
	if err := syncer.Push(context.Background(), "exit", update); err != nil {
		t.Fatal(err)
	}
	header, err := protocol.ReadStreamHeader(&stream.write)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != protocol.FrameTypePublicDirectAuthorization || header.ExitDeviceID != "exit" {
		t.Fatalf("header=%+v", header)
	}
	var sent protocol.PublicDirectAuthorizationUpdate
	if err := protocol.ReadJSON(&stream.write, &sent); err != nil {
		t.Fatal(err)
	}
	if sent != update {
		t.Fatalf("sent=%+v want=%+v", sent, update)
	}
}

func TestAuthorizationSyncerFailsClosedWhenRevocationCannotReachExit(t *testing.T) {
	manager := session.NewManager()
	transport := &authSyncSession{openErr: errors.New("transport failed"), done: make(chan struct{})}
	manager.Register(&session.DeviceSession{
		DeviceID: "exit", PolicyRevision: 7,
		Grants:       []string{protocol.CapabilityProxyExit},
		Capabilities: []string{protocol.CapabilityProxyPublicDirect},
		Tunnel:       transport,
	})
	syncer := &AuthorizationSyncer{Sessions: manager, Timeout: time.Second}
	syncer.RevokeClientFromAllExits(context.Background(), "client")
	if _, ok := manager.Get("exit"); ok {
		t.Fatal("exit remained registered after revocation sync failure")
	}
	if !transport.closed.Load() {
		t.Fatal("exit transport was not closed after revocation sync failure")
	}
}
