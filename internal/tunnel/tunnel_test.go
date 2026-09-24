package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()

	const streamCount = 4
	testMsg := []byte("hello relayproxy stream")
	serverErrCh := make(chan error, 1)
	serverReady := make(chan struct{}, 1)
	clientDone := make(chan struct{})
	go func() {
		rawConn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		serverTLS := tls.Server(rawConn, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		})
		if err := serverTLS.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			serverErrCh <- err
			return
		}
		session, err := ServerTLS(serverTLS, nil)
		if err != nil {
			_ = rawConn.Close()
			serverErrCh <- err
			return
		}
		defer session.Close()
		serverReady <- struct{}{}

		streams := make([]TunnelStream, 0, streamCount)
		for i := 0; i < streamCount; i++ {
			stream, err := session.AcceptStream(ctx)
			if err != nil {
				serverErrCh <- fmt.Errorf("accept stream %d: %w", i, err)
				return
			}
			streams = append(streams, stream)
		}
		defer func() {
			for _, stream := range streams {
				_ = stream.Close()
			}
		}()

		buf := make([]byte, len(testMsg))
		for i, stream := range streams {
			if _, err := io.ReadFull(stream, buf); err != nil {
				serverErrCh <- fmt.Errorf("read stream %d: %w", i, err)
				return
			}
			if _, err := stream.Write(buf); err != nil {
				serverErrCh <- fmt.Errorf("write stream %d: %w", i, err)
				return
			}
		}

		// Writing the final echo only queues bytes into yamux. Do not close the
		// server session until the client confirms it consumed every reply;
		// otherwise the deferred session.Close can race the last client write/read
		// and surface a spurious "session shutdown".
		select {
		case <-clientDone:
			serverErrCh <- nil
		case <-ctx.Done():
			serverErrCh <- ctx.Err()
		}
	}()

	clientSession, err := DialTLS(ctx, listener.Addr().String(), &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS13,
	}, nil)
	if err != nil {
		t.Fatalf("DialTLS failed: %v", err)
	}
	defer clientSession.Close()

	select {
	case <-serverReady:
	case err := <-serverErrCh:
		t.Fatalf("server setup failed: %v", err)
	case <-ctx.Done():
		t.Fatalf("server setup timed out: %v", ctx.Err())
	}

	streams := make([]TunnelStream, 0, streamCount)
	for i := 0; i < streamCount; i++ {
		stream, err := clientSession.OpenStream(ctx)
		if err != nil {
			t.Fatalf("OpenStream %d failed: %v", i, err)
		}
		streams = append(streams, stream)
	}
	defer func() {
		for _, stream := range streams {
			_ = stream.Close()
		}
	}()

	for i, stream := range streams {
		if err := stream.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("SetDeadline stream %d failed: %v", i, err)
		}
		if _, err := stream.Write(testMsg); err != nil {
			select {
			case serverErr := <-serverErrCh:
				t.Fatalf("client stream %d write failed: %v; server: %v", i, err, serverErr)
			default:
				t.Fatalf("client stream %d write failed: %v", i, err)
			}
		}

		reply := make([]byte, len(testMsg))
		if _, err := io.ReadFull(stream, reply); err != nil {
			select {
			case serverErr := <-serverErrCh:
				t.Fatalf("client stream %d read failed: %v; server: %v", i, err, serverErr)
			default:
				t.Fatalf("client stream %d read failed: %v", i, err)
			}
		}
		if string(reply) != string(testMsg) {
			t.Fatalf("stream %d expected %q, got %q", i, testMsg, reply)
		}
	}
	close(clientDone)

	select {
	case err := <-serverErrCh:
		if err != nil {
			t.Fatalf("server encountered error: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("server echo timed out: %v", ctx.Err())
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


func TestDefaultQUICConfigKeepAlivePeriod(t *testing.T) {
	cfg := DefaultQUICConfig()
	if cfg.KeepAlivePeriod != 30*time.Second {
		t.Fatalf("KeepAlivePeriod = %v, want 30s", cfg.KeepAlivePeriod)
	}
}
