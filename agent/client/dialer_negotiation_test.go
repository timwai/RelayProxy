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
	"sync"
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

func (streamOnlySession) Transport() tunnel.TransportType { return tunnel.TransportTLS }

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

func (*namedSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }

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

	selected := dialer.selectedSessionForExit("exit-ready")
	if selected.Session != direct || selected.Path != protocol.ProxyPathP2PQUIC {
		t.Fatalf("ready direct path not selected: selected=%+v", selected)
	}
	selected = dialer.selectedSessionForExit("exit-cold")
	if selected.Session != relay || !selected.Path.IsRelay() {
		t.Fatalf("cold path did not fall back to Relay: selected=%+v", selected)
	}
	if ensureCalls != 1 {
		t.Fatalf("ensure calls=%d, want 1", ensureCalls)
	}
	selected = dialer.selectedSessionForExit("")
	if selected.Session != relay || !selected.Path.IsRelay() {
		t.Fatalf("auto-selected Exit should stay on Relay: selected=%+v", selected)
	}
	if ensureCalls != 1 {
		t.Fatalf("empty Exit unexpectedly started P2P: ensure calls=%d", ensureCalls)
	}
}

func TestTunnelDialerCarriesExplicitPublicDirectPath(t *testing.T) {
	relay := &namedSession{name: "relay"}
	public := &namedSession{name: "public"}
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigurePathProvider(func(exitID string) (SelectedSession, bool) {
		if exitID != "exit" {
			return SelectedSession{}, false
		}
		return SelectedSession{Session: public, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)

	selected := dialer.selectedSessionForExit("exit")
	if selected.Session != public || selected.Path != protocol.ProxyPathPublicDirectQUIC || !selected.IsDirect() {
		t.Fatalf("public direct path was not preserved: %+v", selected)
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

type blockingOpenSession struct {
	*scriptedSession
}

func (s *blockingOpenSession) OpenStream(ctx context.Context) (tunnel.TunnelStream, error) {
	s.opens.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTCPDirectHandshakeTransportFailureFallsBackToRelay(t *testing.T) {
	direct := newScriptedSession(func() tunnel.TunnelStream {
		return &scriptedStream{failWriteAt: 2}
	})
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{Success: true, RemoteIP: "203.0.113.10"})
	})
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
	var fallbacks atomic.Int32
	var failures atomic.Int32
	var callbackOrder []string
	var callbackMu sync.Mutex
	dialer.ConfigureDirectMetrics(func(exitID string) {
		if exitID == "exit" {
			fallbacks.Add(1)
			callbackMu.Lock()
			callbackOrder = append(callbackOrder, "fallback")
			callbackMu.Unlock()
		}
	})
	dialer.ConfigureDirectFailure(func(exitID, reason string) {
		if exitID == "exit" && reason != "" {
			failures.Add(1)
			callbackMu.Lock()
			callbackOrder = append(callbackOrder, "failure")
			callbackMu.Unlock()
		}
	})

	conn, err := dialer.DialTCP(context.Background(), "exit", "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if direct.opens.Load() != 1 || relay.opens.Load() != 1 {
		t.Fatalf("unexpected attempts direct=%d relay=%d", direct.opens.Load(), relay.opens.Load())
	}
	requireTCPPath(t, conn, protocol.P2PPathRelayTLS)
	if fallbacks.Load() != 1 {
		t.Fatalf("fallback metric=%d, want 1", fallbacks.Load())
	}
	if failures.Load() != 1 {
		t.Fatalf("direct failure callback=%d, want 1", failures.Load())
	}
	callbackMu.Lock()
	gotOrder := append([]string(nil), callbackOrder...)
	callbackMu.Unlock()
	if len(gotOrder) != 2 || gotOrder[0] != "fallback" || gotOrder[1] != "failure" {
		t.Fatalf("callback order=%v, want [fallback failure]", gotOrder)
	}
}

func TestDirectAttemptTimeoutPreservesRelayFallbackBudget(t *testing.T) {
	tests := []struct {
		name string
		dial func(*TunnelDialer, context.Context) error
		resp func() tunnel.TunnelStream
	}{
		{
			name: "tcp",
			dial: func(d *TunnelDialer, ctx context.Context) error {
				conn, err := d.DialTCP(ctx, "exit", "example.com", 443)
				if conn != nil {
					_ = conn.Close()
				}
				return err
			},
			resp: func() tunnel.TunnelStream {
				return responseStream(protocol.OpenTCPResponse{Success: true, RemoteIP: "203.0.113.10"})
			},
		},
		{
			name: "udp",
			dial: func(d *TunnelDialer, ctx context.Context) error {
				conn, err := d.DialUDP(ctx, "exit", "203.0.113.53", 53)
				if conn != nil {
					_ = conn.Close()
				}
				return err
			},
			resp: func() tunnel.TunnelStream {
				return responseStream(protocol.OpenUDPResponse{Success: true, Mode: protocol.UDPModeStream})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			direct := &blockingOpenSession{scriptedSession: newScriptedSession(nil)}
			relay := newScriptedSession(tc.resp)
			dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
			dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
			dialer.ConfigureDirectAttemptTimeout(25 * time.Millisecond)

			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := tc.dial(dialer, ctx); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
				t.Fatalf("Relay fallback consumed too much of the caller budget: %v", elapsed)
			}
			if ctx.Err() != nil {
				t.Fatalf("Relay fallback exhausted caller context: %v", ctx.Err())
			}
			if direct.opens.Load() != 1 || relay.opens.Load() != 1 {
				t.Fatalf("unexpected attempts direct=%d relay=%d", direct.opens.Load(), relay.opens.Load())
			}
		})
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
	var failures atomic.Int32
	dialer.ConfigureDirectFailure(func(string, string) { failures.Add(1) })

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
	if failures.Load() != 0 {
		t.Fatalf("business error invalidated direct path %d time(s)", failures.Load())
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

func TestDirectPathPolicyModes(t *testing.T) {
	relay := &namedSession{name: "relay"}
	direct := &namedSession{name: "direct"}

	t.Run("server_exit_always_uses_relay", func(t *testing.T) {
		dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
		var ensure atomic.Int32
		dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, func(string) { ensure.Add(1) })
		dialer.ConfigureDirectPolicy("p2p_only", false)
		selected := dialer.selectedSessionForExit(protocol.ServerExitDeviceID)
		if selected.Session != relay || !selected.Path.IsRelay() || ensure.Load() != 0 {
			t.Fatalf("server exit selected=%+v ensure=%d", selected, ensure.Load())
		}
	})

	t.Run("relay_only", func(t *testing.T) {
		dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
		var ensure atomic.Int32
		dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, func(string) { ensure.Add(1) })
		dialer.ConfigureDirectPolicy("relay_only", true)
		selected := dialer.selectedSessionForExit("exit")
		if selected.Session != relay || !selected.Path.IsRelay() || ensure.Load() != 0 {
			t.Fatalf("relay_only selected=%+v ensure=%d", selected, ensure.Load())
		}
	})

	t.Run("p2p_only_cold", func(t *testing.T) {
		dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
		var ensure atomic.Int32
		dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return nil, false }, func(string) { ensure.Add(1) })
		dialer.ConfigureDirectPolicy("p2p_only", false)
		selected := dialer.selectedSessionForExit("exit")
		if selected.Session != nil || selected.Path != "" || ensure.Load() != 1 {
			t.Fatalf("p2p_only cold selected=%+v ensure=%d", selected, ensure.Load())
		}
	})
}

func TestDirectFallbackCanBeDisabled(t *testing.T) {
	direct := newScriptedSession(func() tunnel.TunnelStream {
		return &scriptedStream{failWriteAt: 2}
	})
	relay := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{Success: true})
	})
	dialer := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	dialer.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
	dialer.ConfigureDirectPolicy("auto", false)
	var fallbacks atomic.Int32
	dialer.ConfigureDirectMetrics(func(string) { fallbacks.Add(1) })

	conn, err := dialer.DialTCP(context.Background(), "exit", "example.com", 443)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("disabled fallback unexpectedly returned Relay connection")
	}
	if err == nil {
		t.Fatal("disabled fallback unexpectedly swallowed direct transport failure")
	}
	if relay.opens.Load() != 0 {
		t.Fatalf("disabled fallback opened Relay %d time(s)", relay.opens.Load())
	}
	if fallbacks.Load() != 0 {
		t.Fatalf("disabled fallback metric=%d, want 0", fallbacks.Load())
	}
}
