package client

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
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
