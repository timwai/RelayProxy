package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// generateSelfSignedCert generates an in-memory TLS certificate for testing
func generateSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa generate key failed: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"RelayProxy Test"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour * 24),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate failed: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  priv,
	}
}

func TestTLSTunnelMultiplexing(t *testing.T) {
	cert := generateSelfSignedCert(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientRaw, serverRaw := net.Pipe()
	clientTLS := tls.Client(clientRaw, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13})
	serverTLS := tls.Server(serverRaw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})

	serverErrCh := make(chan error, 1)
	serverReady := make(chan struct{}, 1)
	go func() {
		if err := serverTLS.HandshakeContext(ctx); err != nil {
			serverErrCh <- err
			return
		}
		session, err := ServerTLS(serverTLS, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer session.Close()
		serverReady <- struct{}{}

		stream, err := session.AcceptStream(ctx)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer stream.Close()

		buf := make([]byte, 1024)
		n, err := stream.Read(buf)
		if err != nil {
			serverErrCh <- err
			return
		}
		_, err = stream.Write(buf[:n])
		serverErrCh <- err
	}()

	if err := clientTLS.HandshakeContext(ctx); err != nil {
		t.Fatalf("client TLS handshake failed: %v", err)
	}
	clientMux, err := yamux.Client(clientTLS, DefaultYAMUXConfig())
	if err != nil {
		t.Fatalf("yamux client init failed: %v", err)
	}
	clientSession := NewTLSSession(clientTLS, clientMux)
	defer clientSession.Close()

	select {
	case <-serverReady:
	case err := <-serverErrCh:
		t.Fatalf("server setup failed: %v", err)
	case <-ctx.Done():
		t.Fatalf("server setup timed out: %v", ctx.Err())
	}

	clientStream, err := clientSession.OpenStream(ctx)
	if err != nil {
		t.Fatalf("OpenStream failed: %v", err)
	}
	defer clientStream.Close()

	testMsg := "hello relayproxy stream"
	if _, err := clientStream.Write([]byte(testMsg)); err != nil {
		t.Fatalf("clientStream.Write failed: %v", err)
	}

	reply := make([]byte, 1024)
	n, err := io.ReadAtLeast(clientStream, reply, len(testMsg))
	if err != nil {
		t.Fatalf("clientStream.Read failed: %v", err)
	}
	if string(reply[:n]) != testMsg {
		t.Fatalf("expected %s, got %s", testMsg, string(reply[:n]))
	}
	if err := <-serverErrCh; err != nil {
		t.Fatalf("server encountered error: %v", err)
	}
}

func TestTLSOpenStreamRespectsCancelledContext(t *testing.T) {
	cert := generateSelfSignedCert(t)
	tlsServerConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	serverReady := make(chan *TLSSession, 1)
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			return
		}
		tlsConn := tls.Server(rawConn, tlsServerConfig)
		session, err := ServerTLS(tlsConn, nil)
		if err != nil {
			_ = rawConn.Close()
			return
		}
		serverReady <- session
		// Never AcceptStream — simulates a stuck/non-accepting peer
		<-session.Done()
		_ = session.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientSession, err := DialTLS(ctx, listener.Addr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatalf("DialTLS failed: %v", err)
	}
	defer clientSession.Close()

	select {
	case sess := <-serverReady:
		defer sess.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("server session not ready")
	}

	cancelled, cancelOpen := context.WithCancel(context.Background())
	cancelOpen()

	stream, err := clientSession.OpenStream(cancelled)
	if err == nil {
		_ = stream.Close()
		t.Fatal("OpenStream returned a stream despite cancelled ctx")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDefaultYAMUXConfigMatchesRelayStreamCapacity(t *testing.T) {
	cfg := DefaultYAMUXConfig()
	if cfg.AcceptBacklog != yamuxAcceptBacklog {
		t.Fatalf("AcceptBacklog=%d want %d", cfg.AcceptBacklog, yamuxAcceptBacklog)
	}
	if cfg.AcceptBacklog < 1024 {
		t.Fatalf("AcceptBacklog=%d is below the Relay default per-device stream capacity", cfg.AcceptBacklog)
	}
}
