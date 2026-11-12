package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	agentexit "relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

type secureRelayCaptureStream struct {
	net.Conn
	mu       sync.Mutex
	received bytes.Buffer
}

func (s *secureRelayCaptureStream) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	if n > 0 {
		s.mu.Lock()
		_, _ = s.received.Write(p[:n])
		s.mu.Unlock()
	}
	return n, err
}

func (s *secureRelayCaptureStream) CloseWrite() error { return s.Conn.Close() }

func (s *secureRelayCaptureStream) captured() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.received.Bytes()...)
}

func secureRelayTestIdentity(t *testing.T, deviceID string) e2ee.Identity {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return e2ee.Identity{DeviceID: deviceID, PublicKey: publicKey, PrivateKey: privateKey}
}

func TestSecureRelayHidesInnerTCPMetadataFromRelay(t *testing.T) {
	clientIdentity := secureRelayTestIdentity(t, "client-device")
	exitIdentity := secureRelayTestIdentity(t, "exit-device")

	relayChecker, err := acl.NewChecker(acl.Policy{
		ID: "server-owned-policy", AllowInternet: true, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	exitChecker, err := acl.NewChecker(acl.Policy{
		ID: "exit-local-policy", AllowInternet: true, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := agentexit.NewHandler(agentexit.HandlerConfig{
		ACLChecker: exitChecker, ConnectTimeout: 2 * time.Second,
		E2EEPublicKey: exitIdentity.PublicKey, E2EEPrivateKey: exitIdentity.PrivateKey,
	})
	t.Cleanup(func() { _ = handler.Close() })

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	serverExitConn, exitConn := net.Pipe()
	defer serverExitConn.Close()
	defer exitConn.Close()
	exitTunnel := &policyTestSession{stream: &policyTestStream{serverExitConn}}
	exitSession := &session.DeviceSession{
		DeviceID: "exit-device", IdentityID: "same-identity",
		Grants: []string{protocol.CapabilityProxyExit},
		Capabilities: []string{protocol.CapabilityE2EEV1, protocol.CapabilityTargetACL},
		PublicKey: append([]byte(nil), exitIdentity.PublicKey...), Tunnel: exitTunnel,
	}
	manager := session.NewManager()
	manager.Register(exitSession)

	audits := make(chan *repository.ConnectionAudit, 1)
	router := NewStreamRouter(manager, relayChecker, func(clientID, exitID string) (bool, error) {
		return clientID == "client-device" && exitID == "exit-device", nil
	}, func(audit *repository.ConnectionAudit) { audits <- audit })

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	capture := &secureRelayCaptureStream{Conn: serverConn}
	clientRaw := &policyTestStream{clientConn}
	clientSession := &session.DeviceSession{
		DeviceID: "client-device", IdentityID: "same-identity",
		Grants: []string{protocol.CapabilityProxyClient},
		Capabilities: []string{protocol.CapabilityE2EEV1},
		PublicKey: append([]byte(nil), clientIdentity.PublicKey...),
	}

	exitDone := make(chan struct{})
	go func() {
		defer close(exitDone)
		header, readErr := protocol.ReadStreamHeader(&policyTestStream{exitConn})
		if readErr != nil {
			return
		}
		handler.HandleStreamWithHeader(agentexit.WithRelayOrigin(context.Background()), &policyTestStream{exitConn}, header)
	}()

	routerDone := make(chan struct{})
	go func() {
		router.HandleClientStream(context.Background(), capture, clientSession)
		close(routerDone)
	}()

	if err := protocol.WriteStreamHeader(clientRaw, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeSecureRelay,
		RequestID: "secure-relay-test", ExitDeviceID: "exit-device",
	}); err != nil {
		t.Fatal(err)
	}
	forgedPolicy := &acl.Policy{
		ID: "client-forged-policy", AllowInternet: true, AllowLoopback: true,
		AccessMode: acl.AccessModeAllow,
	}
	if err := protocol.WriteJSON(clientRaw, protocol.SecureRelayMetadata{
		Version: protocol.SecureRelayVersion, ClientPublicKey: clientIdentity.PublicKey,
		RelayPolicy: forgedPolicy,
	}); err != nil {
		t.Fatal(err)
	}
	secureClient, err := e2ee.ClientHandshake(clientRaw, e2ee.ClientConfig{
		Local: clientIdentity, ExitDeviceID: exitIdentity.DeviceID, ExitPublicKey: exitIdentity.PublicKey,
	})
	if err != nil {
		t.Fatalf("Client E2EE handshake: %v", err)
	}

	if err := protocol.WriteStreamHeader(secureClient, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion,
		Type: protocol.FrameTypeOpenTCP, RequestID: "inner-request", ExitDeviceID: "exit-device",
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(secureClient, protocol.OpenTCPRequest{
		RequestID: "inner-request", Host: "127.0.0.1", Port: uint16(target.Addr().(*net.TCPAddr).Port), TimeoutMs: 1500,
	}); err != nil {
		t.Fatal(err)
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(secureClient, &response); err != nil {
		t.Fatalf("read encrypted OpenTCP response: %v", err)
	}
	if !response.Success {
		t.Fatalf("Exit did not open target: %+v", response)
	}

	payload := []byte("relay-must-not-see-this-secret-payload")
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := secureClient.Write(payload)
		writeDone <- writeErr
	}()
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(secureClient, echo); err != nil {
		t.Fatalf("read encrypted echo: %v", err)
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write encrypted payload: %v", err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("Exit echo mismatch: got %q want %q", echo, payload)
	}
	_ = secureClient.Close()

	select {
	case <-routerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Relay router did not finish secure stream")
	}
	select {
	case <-exitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Exit handler did not finish secure stream")
	}

	captured := capture.captured()
	for _, secret := range [][]byte{[]byte("127.0.0.1"), payload} {
		if bytes.Contains(captured, secret) {
			t.Fatalf("Relay-side stream capture contains plaintext secret %q", secret)
		}
	}
	select {
	case audit := <-audits:
		if audit.Protocol != "secure_relay" || audit.TargetHost != "" || audit.TargetPort != 0 || audit.ResolvedIP != "" {
			t.Fatalf("Relay audit leaked target metadata: %+v", audit)
		}
	case <-time.After(time.Second):
		t.Fatal("Relay secure stream audit was not emitted")
	}
}
