package gateway

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"net"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/cert"
	"relayproxy/internal/deviceidentity"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/repository"
	"relayproxy/server/session"
)

func testGateway(t *testing.T, timeout time.Duration, onAudit func(*repository.ConnectionAudit), authorize ...func(string, protocol.DeviceHello) (DeviceAuthorization, error)) *Gateway {
	t.Helper()
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	authorizer := func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
		return DeviceAuthorization{State: "approved", DeviceID: "client", ApprovedCapabilities: []string{protocol.CapabilityProxyClient}}, nil
	}
	if len(authorize) > 0 {
		authorizer = authorize[0]
	}
	gateway := NewGateway(GatewayConfig{
		TCPAddr: "127.0.0.1:0", TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}},
		HandshakeTimeout: timeout, ServerInstanceID: "test-server", AuthorizeDevice: authorizer,
		RecheckDevice: func(string, string) bool { return true },
	}, sessions, NewStreamRouter(sessions, nil, nil, onAudit))
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	return gateway
}

func dialTestGateway(t *testing.T, gateway *Gateway) tunnel.TunnelSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sess, err := tunnel.DialTLS(ctx, gateway.TCPAddr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

func openTestStream(t *testing.T, sess tunnel.TunnelSession) tunnel.TunnelStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func writeControlHeader(t *testing.T, stream tunnel.TunnelStream) {
	t.Helper()
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeControl,
	}); err != nil {
		t.Fatal(err)
	}
}

func authenticateTestDevice(t *testing.T, control tunnel.TunnelStream, identity *deviceidentity.Identity) protocol.DeviceAccepted {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	hello := protocol.DeviceHello{
		ProtocolVersion: protocol.DeviceProtocolVersion, InstallationID: identity.InstallationID,
		PublicKey: identity.PublicKey, ClientNonce: nonce, DeviceName: "test",
		RequestedCapabilities: []string{protocol.CapabilityProxyClient},
	}
	if err := protocol.WriteJSON(control, hello); err != nil {
		t.Fatal(err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(control, &challenge); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(control, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID, Signature: identity.Sign(protocol.DeviceAuthPayload(hello, challenge)),
	}); err != nil {
		t.Fatal(err)
	}
	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(control, &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

func TestControlHandshakeDeadlineCoversHeaderAndHello(t *testing.T) {
	for _, phase := range []string{"accept", "partial-header", "hello"} {
		t.Run(phase, func(t *testing.T) {
			gateway := testGateway(t, 150*time.Millisecond, nil)
			sess := dialTestGateway(t, gateway)
			if phase != "accept" {
				stream := openTestStream(t, sess)
				if phase == "partial-header" {
					_, _ = stream.Write([]byte{1})
				} else {
					writeControlHeader(t, stream)
				}
			}
			select {
			case <-sess.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("incomplete control handshake outlived its deadline")
			}
			if len(gateway.sessions.List()) != 0 {
				t.Fatal("incomplete handshake registered a device")
			}
		})
	}
}

func TestPendingIdentityCannotRegister(t *testing.T) {
	gateway := testGateway(t, time.Second, nil, func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
		return DeviceAuthorization{State: "pending"}, nil
	})
	identity, _ := deviceidentity.Generate()
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	accepted := authenticateTestDevice(t, control, identity)
	if accepted.Success || accepted.ErrorCode != protocol.ErrCodeApprovalPending || len(gateway.sessions.List()) != 0 {
		t.Fatalf("unexpected pending result: %+v", accepted)
	}
}

func TestInvalidDeviceProofCannotRegister(t *testing.T) {
	gateway := testGateway(t, time.Second, nil)
	identity, _ := deviceidentity.Generate()
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	nonce := make([]byte, 32)
	_, _ = rand.Read(nonce)
	hello := protocol.DeviceHello{ProtocolVersion: protocol.DeviceProtocolVersion, InstallationID: identity.InstallationID,
		PublicKey: identity.PublicKey, ClientNonce: nonce, RequestedCapabilities: []string{protocol.CapabilityProxyClient}}
	if err := protocol.WriteJSON(control, hello); err != nil {
		t.Fatal(err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(control, &challenge); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(control, protocol.AuthProof{ChallengeID: challenge.ChallengeID, Signature: make([]byte, 64)}); err != nil {
		t.Fatal(err)
	}
	var rejected protocol.DeviceAccepted
	if err := protocol.ReadJSON(control, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Success || rejected.ErrorCode != protocol.ErrCodeAuthFailed || len(gateway.sessions.List()) != 0 {
		t.Fatalf("invalid proof was accepted: %+v", rejected)
	}
}

func TestApprovedIdentityRegistersBeforeAcceptance(t *testing.T) {
	gateway := testGateway(t, time.Second, nil)
	identity, _ := deviceidentity.Generate()
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	accepted := authenticateTestDevice(t, control, identity)
	if !accepted.Success || accepted.DeviceID != "client" {
		t.Fatalf("unexpected approval: %+v", accepted)
	}
	if _, ok := gateway.sessions.Get("client"); !ok {
		t.Fatal("acceptance was sent before registration")
	}
}

func TestProxyStreamWithoutClientCapabilityReturnsAccessDenied(t *testing.T) {
	gateway := testGateway(t, time.Second, nil, func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
		return DeviceAuthorization{
			State: "approved", DeviceID: "exit-only",
			ApprovedCapabilities: []string{protocol.CapabilityProxyExit},
		}, nil
	})
	identity, _ := deviceidentity.Generate()
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	if accepted := authenticateTestDevice(t, control, identity); !accepted.Success {
		t.Fatalf("unexpected approval failure: %+v", accepted)
	}

	stream := openTestStream(t, sess)
	_ = stream.SetDeadline(time.Now().Add(time.Second))
	if err := protocol.WriteStreamHeader(stream, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion,
		Type: protocol.FrameTypeOpenTCP, RequestID: "req-denied", ExitDeviceID: "exit-only",
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(stream, protocol.OpenTCPRequest{RequestID: "req-denied", Host: "example.com", Port: 443}); err != nil {
		t.Fatal(err)
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(stream, &response); err != nil {
		t.Fatalf("capability rejection was returned as a transport error: %v", err)
	}
	if response.Success || response.ErrorCode != protocol.ErrCodeAccessDenied {
		t.Fatalf("unexpected capability rejection: %+v", response)
	}
}

func TestCloseReclaimsUnauthenticatedTransports(t *testing.T) {
	gateway := testGateway(t, time.Minute, nil)
	conn, err := net.Dial("tcp", gateway.TCPAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	deadline := time.Now().Add(time.Second)
	for gateway.activeConns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { _ = gateway.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close waited for an unauthenticated handshake timeout")
	}
}

func TestCloseWaitsForAuditProducers(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	gateway := testGateway(t, time.Second, func(*repository.ConnectionAudit) { close(started); <-release })
	identity, _ := deviceidentity.Generate()
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	if accepted := authenticateTestDevice(t, control, identity); !accepted.Success {
		t.Fatalf("handshake failed: %+v", accepted)
	}
	stream := openTestStream(t, sess)
	_ = protocol.WriteStreamHeader(stream, &protocol.StreamHeader{Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenTCP, ExitDeviceID: "offline-exit"})
	_ = protocol.WriteJSON(stream, protocol.OpenTCPRequest{Host: "example.com", Port: 443})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("audit producer did not run")
	}
	done := make(chan struct{})
	go func() { _ = gateway.Close(); close(done) }()
	select {
	case <-done:
		t.Fatal("Close returned before audit producer finished")
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish")
	}
}
