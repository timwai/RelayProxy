package direct_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"relayproxy/agent/client"
	agentdirect "relayproxy/agent/direct"
	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func publicDirectTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	server := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	client := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // test-only; production auth lands with Direct Access Tickets.
	}
	return server, client
}

func startTCPEcho(t *testing.T) (host string, port uint16) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	addr := listener.Addr().(*net.TCPAddr)
	return addr.IP.String(), uint16(addr.Port)
}

func TestPublicDirectTCPUsesExistingExitHandler(t *testing.T) {
	serverTLS, clientTLS := publicDirectTLS(t)
	handler := exit.NewHandler(exit.HandlerConfig{ConnectTimeout: time.Second})
	defer handler.Close()

	publicListener, err := agentdirect.ListenPublic(agentdirect.PublicListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     serverTLS,
		Authorize: func(context.Context, tunnel.TunnelSession) (*acl.Policy, error) {
			return &acl.Policy{
				AllowInternet:       true,
				AllowPrivateNetwork: true,
				AllowLoopback:       true,
			}, nil
		},
	}, handler)
	if err != nil {
		t.Fatal(err)
	}
	defer publicListener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- publicListener.Serve(ctx) }()

	session, err := agentdirect.DialPublic(ctx, publicListener.Addr().String(), agentdirect.PublicClientConfig{
		TLSConfig: clientTLS,
		Authenticate: func(context.Context, tunnel.TunnelSession) error {
			return nil // Phase 2 test seam; Phase 4 replaces this with ticket auth.
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	dialer := client.NewTunnelDialer(nil, nil)
	dialer.ConfigureDirectProvider(func(exitID string) (client.SelectedSession, bool) {
		if exitID != "exit-public" {
			return client.SelectedSession{}, false
		}
		return client.SelectedSession{Session: session, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)

	host, port := startTCPEcho(t)
	conn, err := dialer.DialTCP(ctx, "exit-public", host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	pathSource, ok := conn.(interface{ ProxyPath() string })
	if !ok {
		t.Fatalf("connection %T does not expose proxy path", conn)
	}
	if got := pathSource.ProxyPath(); got != protocol.ProxyPathPublicDirectQUIC.String() {
		t.Fatalf("proxy path = %q, want %q", got, protocol.ProxyPathPublicDirectQUIC)
	}

	payload := []byte("public-direct-tcp")
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("public direct listener did not stop")
	}
}

func TestPublicDirectRequiresAuthorizationHooks(t *testing.T) {
	serverTLS, clientTLS := publicDirectTLS(t)
	handler := exit.NewHandler(exit.HandlerConfig{})
	defer handler.Close()

	if listener, err := agentdirect.ListenPublic(agentdirect.PublicListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     serverTLS,
	}, handler); err == nil || listener != nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatal("public listener started without an authorizer")
	}

	if session, err := agentdirect.DialPublic(context.Background(), net.JoinHostPort("127.0.0.1", strconv.Itoa(9)), agentdirect.PublicClientConfig{
		TLSConfig: clientTLS,
	}); err == nil || session != nil {
		if session != nil {
			_ = session.Close()
		}
		t.Fatal("public client dialed without an authenticator")
	}
}
