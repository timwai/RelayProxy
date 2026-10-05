package direct_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	client "relayproxy/agent/client"
	agentdirect "relayproxy/agent/direct"
	"relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func testTLSConfigs(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "relayproxy-public-direct-test"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	server := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	clientConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // local integration test only; Phase 4 supplies production direct authentication
	}
	return server, clientConfig
}

func startTCPEcho(t *testing.T) (string, uint16) {
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

func startUDPEcho(t *testing.T) (string, uint16) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], remote)
		}
	}()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), uint16(addr.Port)
}

func TestPublicDirectCarriesTCPAndUDPThroughExitHandler(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	handler := exit.NewHandler(exit.HandlerConfig{})
	t.Cleanup(func() { _ = handler.Close() })

	// Phase 2 intentionally uses a test-only no-op authenticator. Production
	// Agent startup does not wire this listener until signed tickets exist.
	testAuth := func(context.Context, tunnel.TunnelSession) error { return nil }
	listener, err := agentdirect.ListenPublicQUIC(
		context.Background(),
		"127.0.0.1:0",
		serverTLS,
		testAuth,
		handler,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := agentdirect.DialPublicQUIC(ctx, listener.Addr().String(), clientTLS, testAuth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	dialer := client.NewTunnelDialer(nil, nil)
	dialer.ConfigureDirectProvider(func(exitID string) (client.SelectedSession, bool) {
		if exitID != "exit-public" {
			return client.SelectedSession{}, false
		}
		return client.SelectedSession{
			Session: session,
			Path:    protocol.ProxyPathPublicDirectQUIC,
		}, true
	}, nil)

	tcpHost, tcpPort := startTCPEcho(t)
	tcpConn, err := dialer.DialTCP(ctx, "exit-public", tcpHost, tcpPort)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("public-direct-tcp")
	if _, err := tcpConn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(tcpConn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("TCP echo = %q, want %q", got, payload)
	}
	if path, ok := tcpConn.(interface{ ProxyPath() string }); !ok || path.ProxyPath() != protocol.ProxyPathPublicDirectQUIC.String() {
		t.Fatalf("TCP connection did not report Public Direct path")
	}
	_ = tcpConn.Close()

	udpHost, udpPort := startUDPEcho(t)
	udpConn, err := dialer.DialUDP(ctx, "exit-public", udpHost, udpPort)
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	udpPayload := []byte("public-direct-udp")
	if _, err := udpConn.WriteTo(udpPayload, nil); err != nil {
		t.Fatal(err)
	}
	_ = udpConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	udpGot := make([]byte, 256)
	n, _, err := udpConn.ReadFrom(udpGot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(udpGot[:n], udpPayload) {
		t.Fatalf("UDP echo = %q, want %q", udpGot[:n], udpPayload)
	}
}

func TestPublicDirectRequiresAuthenticator(t *testing.T) {
	serverTLS, clientTLS := testTLSConfigs(t)
	handler := exit.NewHandler(exit.HandlerConfig{})
	defer handler.Close()

	if listener, err := agentdirect.ListenPublicQUIC(context.Background(), "127.0.0.1:0", serverTLS, nil, handler); err == nil || listener != nil {
		t.Fatal("listener accepted missing authenticator")
	}
	if session, err := agentdirect.DialPublicQUIC(context.Background(), "127.0.0.1:1", clientTLS, nil); err == nil || session != nil {
		t.Fatal("client accepted missing authenticator")
	}
}
