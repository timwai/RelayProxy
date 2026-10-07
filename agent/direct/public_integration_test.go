package direct_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/agent/client"
	"relayproxy/agent/direct"
	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	serverdirect "relayproxy/server/direct"
)

func newTestListener(t *testing.T, ticket []byte) *direct.PublicListener {
	t.Helper()
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := direct.Listen(direct.ListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{identity.Certificate},
		},
		Authenticator: direct.AuthenticatorFunc(func(_ context.Context, request protocol.PublicDirectAuthRequest) error {
			if request.ClientDeviceID != "client" || request.ExitDeviceID != "exit" || !bytes.Equal(request.Ticket, ticket) {
				return errors.New("rejected")
			}
			return nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func testDialConfig(listener *direct.PublicListener, ticket []byte) direct.DialConfig {
	return direct.DialConfig{
		Address:        listener.Addr(),
		TLSConfig:      &tls.Config{InsecureSkipVerify: true},
		ClientDeviceID: "client",
		ExitDeviceID:   "exit",
		Ticket:         ticket,
	}
}

func newSignedTestListener(t *testing.T) (*direct.PublicListener, []byte, *tls.Config) {
	t.Helper()
	checker, err := acl.NewChecker(acl.Policy{
		ID: "relay-test", AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := checker.Policy()
	return newSignedTestListenerWithPolicy(t, &policy)
}

func newSignedTestListenerWithPolicy(t *testing.T, relayPolicy *acl.Policy) (*direct.PublicListener, []byte, *tls.Config) {
	t.Helper()
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	authority, err := serverdirect.NewTicketAuthority("test-server")
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := direct.NewTicketAuthenticator(authority.Issuer(), authority.PublicKey(), "exit")
	if err != nil {
		t.Fatal(err)
	}
	authenticator.SetCurrentValidator(func(context.Context, protocol.PublicDirectTicketClaims) (*acl.Policy, error) {
		if relayPolicy == nil {
			return nil, errors.New("relay policy unavailable")
		}
		copy := *relayPolicy
		return &copy, nil
	})
	listener, err := direct.Listen(direct.ListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     &tls.Config{Certificates: []tls.Certificate{identity.Certificate}},
		Authenticator: authenticator,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ticket, _, err := authority.Issue(serverdirect.TicketIssue{
		ClientDeviceID: "client", ExitDeviceID: "exit",
		PolicyRevision: 3, AuthorizationRevision: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := direct.PinnedTLSConfig(identity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return listener, ticket, pinned
}

func signedDialConfig(listener *direct.PublicListener, ticket []byte, tlsConfig *tls.Config) direct.DialConfig {
	return direct.DialConfig{
		Address: listener.Addr(), TLSConfig: tlsConfig,
		ClientDeviceID: "client", ExitDeviceID: "exit", Ticket: ticket,
	}
}

func TestPublicDirectRejectsInvalidTicket(t *testing.T) {
	ticket := []byte("development-ticket")
	listener := newTestListener(t, ticket)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	accepted := make(chan error, 1)
	go func() {
		_, err := listener.Accept(ctx)
		accepted <- err
	}()
	session, err := direct.Dial(ctx, testDialConfig(listener, []byte("wrong-ticket")))
	if session != nil {
		_ = session.Close()
		t.Fatal("invalid ticket returned a session")
	}
	var relayErr *protocol.RelayError
	if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeAuthFailed {
		t.Fatalf("client auth error=%v", err)
	}
	if err := <-accepted; !errors.Is(err, direct.ErrUnauthorized) {
		t.Fatalf("listener auth error=%v", err)
	}
}

func TestPublicDirectReusesExistingExitHandlerForTCPAndUDP(t *testing.T) {
	listener, ticket, pinnedTLS := newSignedTestListener(t)
	checker, err := acl.NewChecker(acl.Policy{
		AllowInternet:       true,
		AllowPrivateNetwork: true,
		AllowLoopback:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := exit.NewHandler(exit.HandlerConfig{ACLChecker: checker})
	defer handler.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- direct.ServeExit(ctx, listener, handler, 8, 64)
	}()

	session, err := direct.Dial(ctx, signedDialConfig(listener, ticket, pinnedTLS))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	dialer := client.NewTunnelDialer(func() tunnel.TunnelSession { return nil }, nil)
	dialer.ConfigurePathProvider(func(exitID string) (client.SelectedSession, bool) {
		if exitID != "exit" {
			return client.SelectedSession{}, false
		}
		return client.SelectedSession{Session: session, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)

	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	tcpDone := make(chan error, 1)
	go func() {
		conn, err := tcpListener.Accept()
		if err != nil {
			tcpDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		request := make([]byte, 5)
		if _, err := io.ReadFull(conn, request); err != nil {
			tcpDone <- err
			return
		}
		if string(request) != "hello" {
			tcpDone <- errors.New("unexpected tcp request")
			return
		}
		_, err = conn.Write([]byte("world"))
		tcpDone <- err
	}()

	tcpTarget := tcpListener.Addr().(*net.TCPAddr)
	conn, err := dialer.DialTCP(ctx, "exit", "127.0.0.1", uint16(tcpTarget.Port))
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := conn.(interface{ ProxyPath() string }); !ok || source.ProxyPath() != protocol.ProxyPathPublicDirectQUIC.String() {
		_ = conn.Close()
		t.Fatalf("tcp path=%T %#v", conn, source)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	response := make([]byte, 5)
	if _, err := io.ReadFull(conn, response); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.Close()
	if string(response) != "world" {
		t.Fatalf("tcp response=%q", response)
	}
	if err := <-tcpDone; err != nil {
		t.Fatal(err)
	}

	udpTarget, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer udpTarget.Close()
	udpDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 64)
		n, remote, err := udpTarget.ReadFromUDP(buffer)
		if err == nil {
			_, err = udpTarget.WriteToUDP(buffer[:n], remote)
		}
		udpDone <- err
	}()

	packetConn, err := dialer.DialUDP(ctx, "exit", "127.0.0.1", uint16(udpTarget.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	defer packetConn.Close()
	_ = packetConn.SetDeadline(time.Now().Add(3 * time.Second))
	payload := []byte("direct-udp")
	if n, err := packetConn.WriteTo(payload, udpTarget.LocalAddr()); err != nil || n != len(payload) {
		t.Fatalf("udp write n=%d err=%v", n, err)
	}
	buffer := make([]byte, 64)
	n, _, err := packetConn.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("udp response=%q", buffer[:n])
	}
	if err := <-udpDone; err != nil {
		t.Fatal(err)
	}

	cancel()
	_ = listener.Close()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("serve exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("public direct server did not stop")
	}
}

func TestPublicDirectEnforcesServerRelayACL(t *testing.T) {
	relayChecker, err := acl.NewChecker(acl.Policy{
		ID: "relay-deny-loopback", AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	relayPolicy := relayChecker.Policy()
	listener, ticket, pinnedTLS := newSignedTestListenerWithPolicy(t, &relayPolicy)

	localChecker, err := acl.NewChecker(acl.Policy{
		ID: "exit-local-allow", AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := exit.NewHandler(exit.HandlerConfig{ACLChecker: localChecker})
	defer handler.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- direct.ServeExit(ctx, listener, handler, 8, 64)
	}()

	session, err := direct.Dial(ctx, signedDialConfig(listener, ticket, pinnedTLS))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	dialer := client.NewTunnelDialer(func() tunnel.TunnelSession { return nil }, nil)
	dialer.ConfigurePathProvider(func(exitID string) (client.SelectedSession, bool) {
		if exitID != "exit" {
			return client.SelectedSession{}, false
		}
		return client.SelectedSession{Session: session, Path: protocol.ProxyPathPublicDirectQUIC}, true
	}, nil)

	target, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	port := uint16(target.Addr().(*net.TCPAddr).Port)

	conn, err := dialer.DialTCP(ctx, "exit", "127.0.0.1", port)
	if conn != nil {
		_ = conn.Close()
		t.Fatal("server relay ACL unexpectedly allowed loopback over Public Direct")
	}
	var relayErr *protocol.RelayError
	if !errors.As(err, &relayErr) || relayErr.Code != protocol.ErrCodeACLDenied {
		t.Fatalf("public direct ACL error=%v", err)
	}

	_ = target.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if accepted, acceptErr := target.AcceptTCP(); acceptErr == nil {
		_ = accepted.Close()
		t.Fatal("blocked Public Direct request reached the target listener")
	}

	cancel()
	_ = listener.Close()
	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("serve exit: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("public direct server did not stop")
	}
}

func TestPublicDirectSlowHandshakeDoesNotBlockValidClient(t *testing.T) {
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ticket := []byte("development-ticket")
	listener, err := direct.Listen(direct.ListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     &tls.Config{Certificates: []tls.Certificate{identity.Certificate}},
		Authenticator: direct.AuthenticatorFunc(func(_ context.Context, request protocol.PublicDirectAuthRequest) error {
			if request.ClientDeviceID != "client" || request.ExitDeviceID != "exit" || !bytes.Equal(request.Ticket, ticket) {
				return errors.New("rejected")
			}
			return nil
		}),
		AuthTimeout:             2 * time.Second,
		MaxConcurrentHandshakes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	rootCtx, rootCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rootCancel()
	slow, err := tunnel.DialDirectQUIC(rootCtx, listener.Addr(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	// Give the listener enough time to admit the slow connection into one
	// authentication worker. It deliberately never opens the auth stream.
	time.Sleep(50 * time.Millisecond)

	accepted := make(chan *direct.AcceptedSession, 1)
	acceptErr := make(chan error, 1)
	go func() {
		session, err := listener.Accept(rootCtx)
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- session
	}()

	validCtx, validCancel := context.WithTimeout(rootCtx, time.Second)
	defer validCancel()
	client, err := direct.Dial(validCtx, testDialConfig(listener, ticket))
	if err != nil {
		t.Fatalf("valid client was blocked by slow handshake: %v", err)
	}
	defer client.Close()

	select {
	case err := <-acceptErr:
		t.Fatal(err)
	case server := <-accepted:
		if server == nil || server.Tunnel == nil || server.ClientDeviceID != "client" {
			t.Fatalf("accepted session=%+v", server)
		}
		_ = server.Tunnel.Close()
	case <-validCtx.Done():
		t.Fatalf("valid authenticated session was not delivered: %v", validCtx.Err())
	}
}

func TestPublicDirectListenerCloseUnblocksPendingAuthentication(t *testing.T) {
	identity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := direct.Listen(direct.ListenerConfig{
		ListenAddress: "127.0.0.1:0",
		TLSConfig:     &tls.Config{Certificates: []tls.Certificate{identity.Certificate}},
		Authenticator: direct.AuthenticatorFunc(func(context.Context, protocol.PublicDirectAuthRequest) error {
			return nil
		}),
		AuthTimeout:             30 * time.Second,
		MaxConcurrentHandshakes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	slow, err := tunnel.DialDirectQUIC(ctx, listener.Addr(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	defer slow.Close()
	time.Sleep(50 * time.Millisecond)

	closed := make(chan error, 1)
	go func() { closed <- listener.Close() }()
	select {
	case err := <-closed:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("listener close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener close waited for authentication timeout")
	}
}


func TestPublicDirectNegotiatesDirectionalBrutal(t *testing.T) {
	ticket := []byte("brutal-ticket")
	listener := newTestListener(t, ticket)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	acceptedCh := make(chan *direct.AcceptedSession, 1)
	errCh := make(chan error, 1)
	go func() {
		accepted, err := listener.Accept(ctx)
		if err != nil {
			errCh <- err
			return
		}
		acceptedCh <- accepted
	}()

	config := testDialConfig(listener, ticket)
	config.BrutalUploadBPS = 12_500_000
	config.BrutalDownloadBPS = 25_000_000
	clientSession, err := direct.Dial(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	var accepted *direct.AcceptedSession
	select {
	case err := <-errCh:
		t.Fatal(err)
	case accepted = <-acceptedCh:
	}
	if accepted == nil || accepted.Tunnel == nil {
		t.Fatal("missing accepted Public Direct session")
	}
	defer accepted.Tunnel.Close()

	clientDiag := tunnel.DiagnoseSession(clientSession)
	exitDiag := tunnel.DiagnoseSession(accepted.Tunnel)
	if clientDiag == nil || clientDiag.QUIC == nil ||
		clientDiag.QUIC.CongestionController != "brutal" ||
		clientDiag.QUIC.CongestionTargetBPS != 12_500_000 {
		t.Fatalf("client Public Direct congestion = %+v", clientDiag)
	}
	if exitDiag == nil || exitDiag.QUIC == nil ||
		exitDiag.QUIC.CongestionController != "brutal" ||
		exitDiag.QUIC.CongestionTargetBPS != 25_000_000 {
		t.Fatalf("exit Public Direct congestion = %+v", exitDiag)
	}
}
