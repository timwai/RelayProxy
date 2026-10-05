package direct

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"testing"
	"time"

	"relayproxy/internal/tunnel"
)

func directTestTLS(t *testing.T) (*tls.Config, *tls.Config) {
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
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"relayproxy-direct-test"},
		Certificates: []tls.Certificate{cert},
	}, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		NextProtos:         []string{"relayproxy-direct-test"},
		InsecureSkipVerify: true,
	}
}

func TestQUICConfig(t *testing.T) {
	cfg := QUICConfig(QUICOptions{DisableKeepAlive: true, MaxIdleTimeout: 45 * time.Second})
	if !cfg.EnableDatagrams {
		t.Fatal("direct QUIC datagrams are disabled")
	}
	if cfg.KeepAlivePeriod != 0 {
		t.Fatalf("keepalive = %v, want disabled", cfg.KeepAlivePeriod)
	}
	if cfg.MaxIdleTimeout != 45*time.Second {
		t.Fatalf("idle timeout = %v, want 45s", cfg.MaxIdleTimeout)
	}
}

func TestListenAndDialDirectQUIC(t *testing.T) {
	serverTLS, clientTLS := directTestTLS(t)
	listener, err := ListenAddr("127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	accepted := make(chan tunnel.TunnelSession, 1)
	acceptErr := make(chan error, 1)
	go func() {
		session, err := listener.Accept(ctx)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- session
	}()

	client, err := DialAddr(ctx, listener.Addr().String(), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var server tunnel.TunnelSession
	select {
	case err := <-acceptErr:
		t.Fatal(err)
	case server = <-accepted:
		defer server.Close()
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if client.Transport() != tunnel.TransportQUIC || server.Transport() != tunnel.TransportQUIC {
		t.Fatalf("unexpected transport client=%q server=%q", client.Transport(), server.Transport())
	}
	if !tunnel.PeerSupportsDatagrams(client) || !tunnel.PeerSupportsDatagrams(server) {
		t.Fatal("direct QUIC session did not expose datagram capability")
	}
}

func TestDirectQUICRequiresALPN(t *testing.T) {
	_, err := ListenAddr("127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS13})
	if err == nil {
		t.Fatal("listener accepted a TLS config without ALPN")
	}
}
