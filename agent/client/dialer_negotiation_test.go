package client

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func dialerQUICPair(t *testing.T) (tunnel.TunnelSession, tunnel.TunnelSession) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	l, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{"relayproxy-quic"},
	}, &quic.Config{EnableDatagrams: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := tunnel.DialQUIC(ctx, l.Addr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	conn, err := l.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := tunnel.NewQUICSession(conn)
	t.Cleanup(func() { _ = server.Close() })
	tunnel.SetPeerCapabilities(client, []string{protocol.UDPModeDatagram})
	tunnel.SetPeerCapabilities(server, []string{protocol.UDPModeDatagram})
	return client, server
}

func TestDatagramRequiredRejectsLegacySuccessResponse(t *testing.T) {
	for _, mode := range []string{"", protocol.UDPModeStream} {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode=%s/required=%t", mode, required), func(t *testing.T) {
				client, server := dialerQUICPair(t)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				served := make(chan error, 1)
				go func() {
					s, err := server.AcceptStream(ctx)
					if err != nil {
						served <- err
						return
					}
					defer s.Close()
					_ = s.SetDeadline(time.Now().Add(2 * time.Second))
					if _, err := protocol.ReadStreamHeader(s); err != nil {
						served <- err
						return
					}
					var req protocol.OpenUDPRequest
					if err := protocol.ReadJSON(s, &req); err != nil {
						served <- err
						return
					}
					if req.DatagramRequired != required || req.Mode != protocol.UDPModeDatagram || req.AssociationID == 0 {
						served <- fmt.Errorf("invalid UDP request: %+v", req)
						return
					}
					served <- protocol.WriteJSON(s, protocol.OpenUDPResponse{RequestID: req.RequestID, Success: true, Mode: mode})
				}()
				dialer := NewTunnelDialer(func() tunnel.TunnelSession { return client }, nil)
				pc, err := dialer.DialUDPWithOptions(ctx, "exit", "127.0.0.1", 9, UDPDialOptions{DatagramRequired: required})
				if pc != nil {
					_ = pc.Close()
				}
				if required {
					var relayErr *protocol.RelayError
					if pc != nil || !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeDatagramRequired {
						t.Fatalf("required mode accepted downgrade: conn=%v err=%v", pc, err)
					}
				} else if pc == nil || err != nil {
					t.Fatalf("preferred mode rejected legacy response: conn=%v err=%v", pc, err)
				}
				select {
				case err := <-served:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			})
		}
	}
}

type streamOnlySession struct{ tunnel.TunnelSession }

func (streamOnlySession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("unexpected stream open before native capability check")
}

func TestDatagramRequiredRejectsUnsupportedClientTunnel(t *testing.T) {
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return streamOnlySession{} }, nil)
	pc, err := dialer.DialUDPWithOptions(context.Background(), "exit", "127.0.0.1", 9, UDPDialOptions{DatagramRequired: true})
	var relayErr *protocol.RelayError
	if pc != nil || !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeDatagramRequired {
		t.Fatalf("unsupported client tunnel: conn=%v err=%v", pc, err)
	}
}

type namedSession struct {
	tunnel.TunnelSession
	name string
}

func TestTunnelDialerPrefersReadyDirectPathAndWarmsMissingPath(t *testing.T) {
	relay := &namedSession{name: "relay"}
	direct := &namedSession{name: "direct"}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	ensureCalls := 0
	dialer.ConfigureDirectPath(func(exitID string) (tunnel.TunnelSession, bool) {
		if exitID == "exit-ready" {
			return direct, true
		}
		return nil, false
	}, func(exitID string) {
		if exitID != "" {
			ensureCalls++
		}
	})

	session, isDirect := dialer.sessionForExit("exit-ready")
	if session != direct || !isDirect {
		t.Fatalf("ready direct path not selected: session=%v direct=%v", session, isDirect)
	}
	session, isDirect = dialer.sessionForExit("exit-cold")
	if session != relay || isDirect {
		t.Fatalf("cold path did not fall back to Relay: session=%v direct=%v", session, isDirect)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensure calls=%d, want 1", ensureCalls)
	}
	session, isDirect = dialer.sessionForExit("")
	if session != relay || isDirect {
		t.Fatalf("auto-selected Exit should stay on Relay: session=%v direct=%v", session, isDirect)
	}
	if ensureCalls != 1 {
		t.Fatalf("empty Exit unexpectedly started P2P: ensure calls=%d", ensureCalls)
	}
}

type scriptedStream struct {
	read        bytes.Buffer
	failWriteAt int32
	writes      atomic.Int32
	closed      atomic.Bool
}

func responseStream(value any) *scriptedStream {
	stream := &scriptedStream{}
	_ = protocol.WriteJSON(&stream.read, value)
	return stream
}

func (s *scriptedStream) Read(p []byte) (int, error) {
	return s.read.Read(p)
}

func (s *scriptedStream) Write(p []byte) (int, error) {
	count := s.writes.Add(1)
	if s.failWriteAt > 0 && count == s.failWriteAt {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func (s *scriptedStream) Close() error {
	s.closed.Store(true)
	return nil
}

func (s *scriptedStream) CloseWrite() error                { return nil }
func (s *scriptedStream) SetDeadline(time.Time) error      { return nil }
func (s *scriptedStream) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptedStream) SetWriteDeadline(time.Time) error { return nil }

type scriptedSession struct {
	factory func() tunnel.TunnelStream
	opens   atomic.Int32
	done    chan struct{}
}

func newScriptedSession(factory func() tunnel.TunnelStream) *scriptedSession {
	return &scriptedSession{factory: factory, done: make(chan struct{})}
}

func (s *scriptedSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	s.opens.Add(1)
	if s.factory == nil {
		return nil, errors.New("no stream factory")
	}
	return s.factory(), nil
}

func (s *scriptedSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *scriptedSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *scriptedSession) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 443}
}
func (s *scriptedSession) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 40000}
}
func (s *scriptedSession) Close() error          { return nil }
func (s *scriptedSession) Done() <-chan struct{} { return s.done }

func TestTCPDirectHandshakeTransportFailureFallsBackToRelay(t *testing.T) {
	direct := newScriptedSession(func() tunnel.TunnelStream {
		return &scriptedStream{failWriteAt: 2}
	})
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{Success: true, RemoteIP: "203.0.113.10"})
	})
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)

	conn, err := dialer.DialTCP(context.Background(), "exit", "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if direct.opens.Load() != 1 || relay.opens.Load() != 1 {
		t.Fatalf("unexpected attempts direct=%d relay=%d", direct.opens.Load(), relay.opens.Load())
	}
}

func TestTCPDirectBusinessErrorDoesNotRetryRelay(t *testing.T) {
	direct := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{
			Success: false, ErrorCode: protocol.ErrCodeACLDenied, ErrorMessage: "blocked",
		})
	})
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{Success: true})
	})
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)

	conn, err := dialer.DialTCP(context.Background(), "exit", "blocked.example", 443)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("business error unexpectedly returned a connection")
	}
	var relayErr *protocol.RelayError
	if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeACLDenied {
		t.Fatalf("unexpected error: %v", err)
	}
	if relay.opens.Load() != 0 {
		t.Fatalf("business error retried Relay %d time(s)", relay.opens.Load())
	}
}

func TestUDPDirectHandshakeTransportFailureFallsBackToRelay(t *testing.T) {
	direct := newScriptedSession(func() tunnel.TunnelStream {
		return &scriptedStream{failWriteAt: 2}
	})
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenUDPResponse{Success: true, Mode: protocol.UDPModeStream})
	})
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)

	conn, err := dialer.DialUDP(context.Background(), "exit", "203.0.113.53", 53)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if direct.opens.Load() != 1 || relay.opens.Load() != 1 {
		t.Fatalf("unexpected attempts direct=%d relay=%d", direct.opens.Load(), relay.opens.Load())
	}
}
