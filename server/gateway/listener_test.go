package gateway

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
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
		AllowLegacyDeviceAuth: true,
		RecheckDevice:         func(string, string) bool { return true },
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
		ProtocolVersion: protocol.LegacyDeviceProtocolVersion, InstallationID: identity.InstallationID,
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

func authenticateTestDeviceHello(t *testing.T, control tunnel.TunnelStream, identity *deviceidentity.Identity, hello protocol.DeviceHello) protocol.DeviceAccepted {
	t.Helper()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	hello.ProtocolVersion = protocol.LegacyDeviceProtocolVersion
	hello.InstallationID = identity.InstallationID
	hello.PublicKey = identity.PublicKey
	hello.ClientNonce = nonce
	if err := protocol.WriteJSON(control, hello); err != nil {
		t.Fatal(err)
	}
	var challenge protocol.AuthChallenge
	if err := protocol.ReadJSON(control, &challenge); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(control, protocol.AuthProof{
		ChallengeID: challenge.ChallengeID,
		Signature:   identity.Sign(protocol.DeviceAuthPayload(hello, challenge)),
	}); err != nil {
		t.Fatal(err)
	}
	var accepted protocol.DeviceAccepted
	if err := protocol.ReadJSON(control, &accepted); err != nil {
		t.Fatal(err)
	}
	return accepted
}

func readInventoryPush(t *testing.T, sess tunnel.TunnelSession) protocol.ResourceInventory {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := sess.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(time.Second))
	header, err := protocol.ReadStreamHeader(stream)
	if err != nil {
		t.Fatal(err)
	}
	if header.Type != protocol.FrameTypeResourceInventory {
		t.Fatalf("push frame type=%d, want resource inventory", header.Type)
	}
	var inventory protocol.ResourceInventory
	if err := protocol.ReadJSON(stream, &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
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
	hello := protocol.DeviceHello{ProtocolVersion: protocol.LegacyDeviceProtocolVersion, InstallationID: identity.InstallationID,
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

func TestIdentityRequiredRejectsLegacyProtocolBeforeChallenge(t *testing.T) {
	gateway := testGateway(t, time.Second, nil)
	gateway.cfg.AllowLegacyDeviceAuth = false
	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(control, protocol.DeviceHello{
		ProtocolVersion: protocol.LegacyDeviceProtocolVersion,
		InstallationID:  identity.InstallationID, PublicKey: identity.PublicKey,
		ClientNonce: nonce, RequestedCapabilities: []string{protocol.CapabilityProxyClient},
	}); err != nil {
		t.Fatal(err)
	}
	var rejected protocol.DeviceAccepted
	if err := protocol.ReadJSON(control, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Success || rejected.ErrorCode != protocol.ErrCodeIdentityInvalid || len(gateway.sessions.List()) != 0 {
		t.Fatalf("legacy protocol was not rejected for identity-only server: %+v", rejected)
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

func TestApprovedIdentityReceivesAuthorizedRDPTargets(t *testing.T) {
	gateway := testGateway(t, time.Second, nil, func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
		return DeviceAuthorization{
			State: "approved", DeviceID: "controller",
			ApprovedCapabilities: []string{protocol.CapabilityRDPClient},
			RDPTargets:           []protocol.RDPTarget{{DeviceID: "target", Name: "Office PC", Online: true}},
		}, nil
	})
	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	accepted := authenticateTestDevice(t, control, identity)
	if !accepted.Success || len(accepted.RDPTargets) != 1 || accepted.RDPTargets[0].DeviceID != "target" || !accepted.RDPTargets[0].Online {
		t.Fatalf("authorized RDP inventory was not sent: %+v", accepted)
	}
}

func TestHeartbeatRefreshesAuthorizedRDPTargets(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	var mu sync.Mutex
	targets := []protocol.RDPTarget{{DeviceID: "target", Name: "Office PC", Online: true}}
	heartbeats := make(chan string, 2)
	gateway := NewGateway(GatewayConfig{
		TCPAddr: "127.0.0.1:0", TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}},
		HandshakeTimeout: time.Second, ServerInstanceID: "test-server",
		AllowLegacyDeviceAuth: true,
		AuthorizeDevice: func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
			return DeviceAuthorization{State: "approved", DeviceID: "controller", ApprovedCapabilities: []string{protocol.CapabilityRDPClient}}, nil
		},
		RecheckDevice: func(string, string) bool { return true },
		ListRDPTargets: func(controllerID string) ([]protocol.RDPTarget, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]protocol.RDPTarget(nil), targets...), nil
		},
		OnDeviceHeartbeat: func(deviceID string) { heartbeats <- deviceID },
	}, sessions, NewStreamRouter(sessions, nil, nil, nil))
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	if accepted := authenticateTestDevice(t, control, identity); !accepted.Success {
		t.Fatalf("unexpected approval failure: %+v", accepted)
	}

	readTargets := func() []protocol.RDPTarget {
		t.Helper()
		if err := protocol.WriteJSON(control, protocol.PingMessage{
			Timestamp: time.Now().UnixMilli(), Diagnostics: json.RawMessage(`{"status":{"mode":"CLIENT"}}`),
		}); err != nil {
			t.Fatal(err)
		}
		var pong protocol.PongMessage
		if err := protocol.ReadJSON(control, &pong); err != nil {
			t.Fatal(err)
		}
		if pong.RDPTargets == nil {
			t.Fatal("RDP target refresh was omitted")
		}
		return *pong.RDPTargets
	}

	if got := readTargets(); len(got) != 1 || got[0].DeviceID != "target" || !got[0].Online {
		t.Fatalf("first refresh = %+v", got)
	}
	device, ok := sessions.Get("controller")
	if !ok {
		t.Fatal("controller session was not registered")
	}
	if diagnostics := device.DiagnosticsSnapshot(); diagnostics == nil || string(diagnostics.Payload) != `{"status":{"mode":"CLIENT"}}` {
		t.Fatalf("heartbeat diagnostics were not stored: %+v", diagnostics)
	}
	mu.Lock()
	targets = []protocol.RDPTarget{}
	mu.Unlock()
	if got := readTargets(); len(got) != 0 {
		t.Fatalf("revoked targets were retained: %+v", got)
	}
	for range 2 {
		select {
		case got := <-heartbeats:
			if got != "controller" {
				t.Fatalf("heartbeat callback device = %q", got)
			}
		case <-time.After(time.Second):
			t.Fatal("heartbeat callback was not invoked")
		}
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

func TestHeartbeatForDeviceAndroidExit(t *testing.T) {
	g := &Gateway{cfg: GatewayConfig{HeartbeatSec: 15}}

	got := g.heartbeatForDevice(protocol.DeviceHello{Platform: "android"}, []string{protocol.CapabilityProxyExit})
	if got != 30 {
		t.Fatalf("android exit heartbeat = %d, want 30", got)
	}

	g.cfg.HeartbeatSec = 45
	got = g.heartbeatForDevice(protocol.DeviceHello{Platform: "android"}, []string{protocol.CapabilityProxyExit})
	if got != 45 {
		t.Fatalf("configured android exit heartbeat = %d, want 45", got)
	}
}

func TestHeartbeatForDeviceKeepsDefaultForOtherClients(t *testing.T) {
	g := &Gateway{cfg: GatewayConfig{HeartbeatSec: 15}}

	cases := []struct {
		name       string
		hello      protocol.DeviceHello
		capability []string
	}{
		{name: "desktop exit", hello: protocol.DeviceHello{Platform: "windows"}, capability: []string{protocol.CapabilityProxyExit}},
		{name: "android client only", hello: protocol.DeviceHello{Platform: "android"}, capability: []string{protocol.CapabilityProxyClient}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.heartbeatForDevice(tc.hello, tc.capability); got != 15 {
				t.Fatalf("heartbeat = %d, want 15", got)
			}
		})
	}
}

func TestActiveRuntimeCapabilitiesAreSignedAndGrantBound(t *testing.T) {
	approved := []string{protocol.CapabilityProxyClient, protocol.CapabilityProxyExit}
	active := activeRuntimeCapabilities([]string{
		protocol.CapabilityRuntimeState,
		protocol.CapabilityProxyClientActive,
	}, approved)
	if len(active) != 1 || active[0] != protocol.CapabilityProxyClient {
		t.Fatalf("active runtime capabilities = %v", active)
	}

	idle := activeRuntimeCapabilities([]string{protocol.CapabilityRuntimeState}, approved)
	if idle == nil || len(idle) != 0 {
		t.Fatalf("control-only runtime capabilities = %#v, want non-nil empty", idle)
	}

	legacy := activeRuntimeCapabilities(nil, approved)
	if legacy != nil {
		t.Fatalf("legacy runtime capabilities = %#v, want nil fallback", legacy)
	}
	if got := runtimeCapabilitiesOrGrants(legacy, approved); len(got) != len(approved) {
		t.Fatalf("legacy grants fallback = %v", got)
	}
}

func TestWelcomeDistributesP2PPortRange(t *testing.T) {
	gateway := testGateway(t, time.Second, nil)
	gateway.cfg.P2PEnabled = true
	gateway.cfg.P2PPortStart = 30000
	gateway.cfg.P2PPortEnd = 30100
	gateway.cfg.P2PUPnPEnabled = true

	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	accepted := authenticateTestDevice(t, control, identity)
	if !accepted.Success {
		t.Fatalf("device was not accepted: %+v", accepted)
	}
	if accepted.P2PPortStart != 30000 || accepted.P2PPortEnd != 30100 {
		t.Fatalf("P2P port range=%d-%d, want 30000-30100", accepted.P2PPortStart, accepted.P2PPortEnd)
	}
	if !accepted.P2PUPnPEnabled {
		t.Fatal("P2P UPnP setting was not distributed")
	}
}

func TestWelcomeDistributesPublicDirectPolicy(t *testing.T) {
	gateway := testGateway(t, time.Second, nil)
	gateway.cfg.PublicDirectEnabled = true
	gateway.cfg.PublicDirectTicketIssuer = "relay-test"
	gateway.cfg.PublicDirectTicketKey = []byte{1, 2, 3, 4}
	gateway.cfg.PublicDirectPortStart = 31000
	gateway.cfg.PublicDirectPortEnd = 31100

	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	sess := dialTestGateway(t, gateway)
	control := openTestStream(t, sess)
	writeControlHeader(t, control)
	accepted := authenticateTestDevice(t, control, identity)
	if !accepted.Success {
		t.Fatalf("device was not accepted: %+v", accepted)
	}
	if !containsCapability(accepted.TransportCapabilities, protocol.CapabilityProxyPublicDirect) {
		t.Fatalf("Public Direct capability not advertised: %v", accepted.TransportCapabilities)
	}
	if accepted.PublicDirectTicketIssuer != "relay-test" || len(accepted.PublicDirectTicketKey) != 4 {
		t.Fatalf("Public Direct ticket verification material missing: %+v", accepted)
	}
	if accepted.PublicDirectPortStart != 31000 || accepted.PublicDirectPortEnd != 31100 {
		t.Fatalf("Public Direct port range=%d-%d, want 31000-31100", accepted.PublicDirectPortStart, accepted.PublicDirectPortEnd)
	}
}

func TestExitLifecyclePushesProxyInventoryImmediately(t *testing.T) {
	gateway := testGateway(t, time.Second, nil, func(_ string, hello protocol.DeviceHello) (DeviceAuthorization, error) {
		switch hello.DeviceName {
		case "client":
			return DeviceAuthorization{
				State: "approved", DeviceID: "client",
				ApprovedCapabilities: []string{protocol.CapabilityProxyClient},
			}, nil
		case "exit":
			return DeviceAuthorization{
				State: "approved", DeviceID: "exit-a",
				ApprovedCapabilities: []string{protocol.CapabilityProxyExit},
			}, nil
		default:
			return DeviceAuthorization{State: "rejected"}, nil
		}
	})
	gateway.cfg.ListProxyExits = func(clientID, _, _ string) ([]protocol.ProxyExit, error) {
		exits := gateway.sessions.GetExits()
		result := make([]protocol.ProxyExit, 0, len(exits))
		for _, item := range exits {
			if item == nil || item.DeviceID == "" || item.DeviceID == clientID {
				continue
			}
			result = append(result, protocol.ProxyExit{
				DeviceID: item.DeviceID,
				Name:     item.DeviceName,
				Online:   true,
			})
		}
		return result, nil
	}

	clientIdentity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	clientSession := dialTestGateway(t, gateway)
	clientControl := openTestStream(t, clientSession)
	writeControlHeader(t, clientControl)
	clientAccepted := authenticateTestDeviceHello(t, clientControl, clientIdentity, protocol.DeviceHello{
		DeviceName:            "client",
		RequestedCapabilities: []string{protocol.CapabilityProxyClient},
		TransportCapabilities: []string{protocol.CapabilityResourceInventoryPush},
	})
	if !clientAccepted.Success {
		t.Fatalf("client was not accepted: %+v", clientAccepted)
	}
	if clientAccepted.ProxyExits == nil || len(*clientAccepted.ProxyExits) != 0 {
		t.Fatalf("client startup inventory=%+v, want explicit empty list", clientAccepted.ProxyExits)
	}
	initialRevision := clientAccepted.ProxyExitRevision
	if initialRevision == 0 {
		t.Fatal("client startup inventory revision was not set")
	}

	manualRevision := gateway.RefreshProxyExitInventories()
	manual := readInventoryPush(t, clientSession)
	if manual.ProxyExits == nil || len(*manual.ProxyExits) != 0 {
		t.Fatalf("manual refresh inventory=%+v, want explicit empty list", manual.ProxyExits)
	}
	if manual.ProxyExitRevision != manualRevision || manualRevision <= initialRevision {
		t.Fatalf("manual revision=%d push=%d initial=%d", manualRevision, manual.ProxyExitRevision, initialRevision)
	}
	initialRevision = manualRevision

	exitIdentity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	exitSession := dialTestGateway(t, gateway)
	exitControl := openTestStream(t, exitSession)
	writeControlHeader(t, exitControl)
	exitAccepted := authenticateTestDeviceHello(t, exitControl, exitIdentity, protocol.DeviceHello{
		DeviceName:            "exit",
		RequestedCapabilities: []string{protocol.CapabilityProxyExit},
	})
	if !exitAccepted.Success {
		t.Fatalf("exit was not accepted: %+v", exitAccepted)
	}

	online := readInventoryPush(t, clientSession)
	if online.ProxyExits == nil || len(*online.ProxyExits) != 1 ||
		(*online.ProxyExits)[0].DeviceID != "exit-a" || !(*online.ProxyExits)[0].Online {
		t.Fatalf("online exit push=%+v", online)
	}
	if online.ProxyExitRevision <= initialRevision {
		t.Fatalf("online revision=%d, initial=%d", online.ProxyExitRevision, initialRevision)
	}

	// Closing an already-draining multiplexed transport may report the
	// underlying network close; the lifecycle signal is the server-side
	// unregister and the inventory push that follows.
	_ = exitSession.Close()
	offline := readInventoryPush(t, clientSession)
	if offline.ProxyExits == nil || len(*offline.ProxyExits) != 0 {
		t.Fatalf("offline exit push retained inventory: %+v", offline)
	}
	if offline.ProxyExitRevision <= online.ProxyExitRevision {
		t.Fatalf("offline revision=%d, online=%d", offline.ProxyExitRevision, online.ProxyExitRevision)
	}
}

func TestNegotiatedBrutalRates(t *testing.T) {
	tests := []struct {
		name                       string
		transport                  tunnel.TransportType
		hello                      protocol.DeviceHello
		cfg                        GatewayConfig
		wantClientTx, wantServerTx uint64
	}{
		{
			name:         "quic clamps both directions",
			transport:    tunnel.TransportQUIC,
			hello:        protocol.DeviceHello{BrutalUploadBPS: 20_000_000, BrutalDownloadBPS: 40_000_000},
			cfg:          GatewayConfig{BrutalMaxUploadBPS: 30_000_000, BrutalMaxDownloadBPS: 10_000_000},
			wantClientTx: 10_000_000, wantServerTx: 30_000_000,
		},
		{
			name:         "zero server caps leave client hints unchanged",
			transport:    tunnel.TransportQUIC,
			hello:        protocol.DeviceHello{BrutalUploadBPS: 20_000_000, BrutalDownloadBPS: 40_000_000},
			wantClientTx: 20_000_000, wantServerTx: 40_000_000,
		},
		{
			name:         "one zero hint keeps that direction on BBR",
			transport:    tunnel.TransportQUIC,
			hello:        protocol.DeviceHello{BrutalDownloadBPS: 40_000_000},
			wantServerTx: 40_000_000,
		},
		{
			name:      "TLS never negotiates Brutal",
			transport: tunnel.TransportTLS,
			hello:     protocol.DeviceHello{BrutalUploadBPS: 20_000_000, BrutalDownloadBPS: 40_000_000},
		},
		{
			name:      "server can ignore client bandwidth",
			transport: tunnel.TransportQUIC,
			hello:     protocol.DeviceHello{BrutalUploadBPS: 20_000_000, BrutalDownloadBPS: 40_000_000},
			cfg:       GatewayConfig{IgnoreClientBandwidth: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientTx, serverTx := negotiatedBrutalRates(tt.transport, tt.hello, tt.cfg)
			if clientTx != tt.wantClientTx || serverTx != tt.wantServerTx {
				t.Fatalf("rates client/server=%d/%d, want %d/%d", clientTx, serverTx, tt.wantClientTx, tt.wantServerTx)
			}
		})
	}
}

func TestQUICHandshakeNegotiatesAndAppliesBrutal(t *testing.T) {
	certificate, err := cert.EnsureCertificate("", "", "localhost")
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	gateway := NewGateway(GatewayConfig{
		QUICAddr:              "127.0.0.1:0",
		TLSConfig:             &tls.Config{Certificates: []tls.Certificate{certificate}},
		HandshakeTimeout:      2 * time.Second,
		ServerInstanceID:      "brutal-test-server",
		AllowLegacyDeviceAuth: true,
		BrutalMaxUploadBPS:    25_000_000,
		BrutalMaxDownloadBPS:  15_000_000,
		AuthorizeDevice: func(string, protocol.DeviceHello) (DeviceAuthorization, error) {
			return DeviceAuthorization{
				State: "approved", DeviceID: "client",
				ApprovedCapabilities: []string{protocol.CapabilityProxyClient},
			}, nil
		},
		RecheckDevice: func(string, string) bool { return true },
	}, sessions, NewStreamRouter(sessions, nil, nil, nil))
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	clientSession, err := tunnel.DialQUIC(ctx, gateway.QUICAddr().String(), &tls.Config{InsecureSkipVerify: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	control := openTestStream(t, clientSession)
	writeControlHeader(t, control)
	identity, err := deviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	accepted := authenticateTestDeviceHello(t, control, identity, protocol.DeviceHello{
		DeviceName:            "brutal-client",
		RequestedCapabilities: []string{protocol.CapabilityProxyClient},
		BrutalUploadBPS:       20_000_000,
		BrutalDownloadBPS:     30_000_000,
	})
	if !accepted.Success {
		t.Fatalf("unexpected approval failure: %+v", accepted)
	}
	if accepted.BrutalUploadBPS != 15_000_000 || accepted.BrutalDownloadBPS != 25_000_000 {
		t.Fatalf("negotiated rates upload/download=%d/%d, want 15000000/25000000",
			accepted.BrutalUploadBPS, accepted.BrutalDownloadBPS)
	}

	serverSession, ok := sessions.Get("client")
	if !ok || serverSession == nil {
		t.Fatal("authenticated QUIC session was not registered")
	}
	diagnostics := tunnel.DiagnoseSession(serverSession.Tunnel)
	if diagnostics == nil || diagnostics.QUIC == nil {
		t.Fatal("missing server QUIC diagnostics")
	}
	if diagnostics.QUIC.CongestionController != "brutal" ||
		diagnostics.QUIC.CongestionTargetBPS != 25_000_000 {
		t.Fatalf("server congestion diagnostics = %+v", diagnostics.QUIC)
	}
}
