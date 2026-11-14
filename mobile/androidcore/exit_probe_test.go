package androidcore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentclient "relayproxy/agent/client"
	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/e2ee"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type androidProbeStream struct {
	bytes.Buffer
	onHalfClose func()
}

func (s *androidProbeStream) Write(p []byte) (int, error) { return len(p), nil }
func (s *androidProbeStream) Close() error                { return nil }
func (s *androidProbeStream) CloseWrite() error {
	if s.onHalfClose != nil {
		s.onHalfClose()
	}
	return nil
}
func (s *androidProbeStream) SetDeadline(time.Time) error      { return nil }
func (s *androidProbeStream) SetReadDeadline(time.Time) error  { return nil }
func (s *androidProbeStream) SetWriteDeadline(time.Time) error { return nil }

type androidPipeTunnelStream struct {
	net.Conn
}

func (s *androidPipeTunnelStream) CloseWrite() error { return s.Conn.Close() }

type androidProbeSession struct {
	opens    atomic.Int32
	stream   tunnel.TunnelStream
	incoming tunnel.TunnelStream
	accepted atomic.Bool
}

func (s *androidProbeSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	s.opens.Add(1)
	return s.stream, nil
}
func (s *androidProbeSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	if s.incoming != nil && s.accepted.CompareAndSwap(false, true) {
		return s.incoming, nil
	}
	return nil, io.EOF
}

type androidIncomingStream struct {
	androidProbeStream
	output bytes.Buffer
	closed bool
}

func (s *androidIncomingStream) Write(p []byte) (int, error) { return s.output.Write(p) }
func (s *androidIncomingStream) Close() error                { s.closed = true; return nil }

func TestAndroidExitAcceptsE2EESecureRelayFromRelayAndP2P(t *testing.T) {
	for _, route := range []string{"relay", "direct"} {
		t.Run(route, func(t *testing.T) {
			identityPath := filepath.Join(t.TempDir(), "device-identity.json")
			android, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, identityPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = android.Stop() })

			clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			const clientID, exitID = "probe-client", "android-exit"
			serverConn, clientConn := net.Pipe()
			serverStream := &androidPipeTunnelStream{Conn: serverConn}
			clientStream := &androidPipeTunnelStream{Conn: clientConn}
			_ = serverStream.SetDeadline(time.Now().Add(10 * time.Second))
			_ = clientStream.SetDeadline(time.Now().Add(10 * time.Second))
			t.Cleanup(func() {
				_ = serverStream.Close()
				_ = clientStream.Close()
			})
			session := &androidProbeSession{incoming: serverStream}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			acceptDone := make(chan struct{})
			go func() {
				if route == "relay" {
					var workers sync.WaitGroup
					android.acceptIncomingStreams(ctx, session, 1, true, nil, &workers)
					workers.Wait()
				} else {
					android.acceptProxyP2PExitSession(ctx, session, &acl.Policy{}, clientID, exitID, 1)
				}
				close(acceptDone)
			}()

			clientDone := make(chan error, 1)
			go func() {
				if err := protocol.WriteStreamHeader(clientStream, &protocol.StreamHeader{
					Type: protocol.FrameTypeSecureRelay, RequestID: "android-e2ee-probe",
					ClientDeviceID: clientID, ExitDeviceID: exitID,
				}); err != nil {
					clientDone <- err
					return
				}
				if err := protocol.WriteJSON(clientStream, protocol.SecureRelayMetadata{
					Version: protocol.SecureRelayVersion, ClientPublicKey: clientPublic,
				}); err != nil {
					clientDone <- err
					return
				}
				secure, err := e2ee.ClientHandshake(clientStream, e2ee.ClientConfig{
					Local:        e2ee.Identity{DeviceID: clientID, PublicKey: clientPublic, PrivateKey: clientPrivate},
					ExitDeviceID: exitID, ExitPublicKey: android.identity.PublicKey,
				})
				if err != nil {
					clientDone <- err
					return
				}
				clientDone <- secure.Close()
			}()
			select {
			case err := <-clientDone:
				if err != nil {
					t.Fatalf("Android Exit E2EE handshake failed on %s path: %v", route, err)
				}
			case <-ctx.Done():
				t.Fatalf("Android Exit E2EE handshake timed out on %s path", route)
			}
			select {
			case <-acceptDone:
			case <-ctx.Done():
				t.Fatalf("Android Exit did not finish SecureRelay handling on %s path", route)
			}
		})
	}
}

func TestAndroidExitServesLatencyProbeThroughRelayAndDirect(t *testing.T) {
	for _, route := range []string{"relay", "direct", "relay-unapproved", "direct-unbound"} {
		t.Run(route, func(t *testing.T) {
			stream := &androidIncomingStream{}
			_ = protocol.WriteStreamHeader(&stream.Buffer, &protocol.StreamHeader{Type: protocol.FrameTypeSpeedTest, RequestID: "probe"})
			_ = protocol.WriteJSON(&stream.Buffer, protocol.SpeedTestRequest{RequestID: "probe", Direction: protocol.SpeedTestUpload, DurationMS: 500})
			sess := &androidProbeSession{incoming: stream}
			c := &Client{handler: exit.NewHandler(exit.HandlerConfig{})}
			defer c.handler.Close()
			var workers sync.WaitGroup
			switch route {
			case "relay", "relay-unapproved":
				c.acceptIncomingStreams(context.Background(), sess, 1, route == "relay", nil, &workers)
				workers.Wait()
			case "direct":
				c.acceptProxyP2PExitSession(context.Background(), sess, &acl.Policy{}, "probe-client", "probe-exit", 1)
			case "direct-unbound":
				c.acceptProxyP2PExitSession(context.Background(), sess, nil, "probe-client", "probe-exit", 1)
			}
			if route == "relay-unapproved" || route == "direct-unbound" {
				if stream.output.Len() != 0 {
					t.Fatal("unauthorized exit served a probe")
				}
				return
			}
			var ack protocol.SpeedTestResponse
			if err := protocol.ReadJSON(&stream.output, &ack); err != nil || !ack.Success || ack.RequestID != "probe" {
				t.Fatalf("exit did not acknowledge probe: %+v / %v", ack, err)
			}
			var complete protocol.SpeedTestComplete
			if err := protocol.ReadJSON(&stream.output, &complete); err != nil || complete.Bytes != 0 || !stream.closed {
				t.Fatalf("exit did not cleanly finish probe: %+v / %v", complete, err)
			}
		})
	}
}

func TestAndroidHelloAdvertisesExitProbeCapability(t *testing.T) {
	c, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","clientEnabled":true}`, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	stream := &androidIncomingStream{}
	sess := &androidProbeSession{stream: stream}
	// EOF after Hello intentionally ends authentication. Inspect the real wire
	// hello so a future capability regression cannot hide behind probe doubles.
	_ = c.serveSession(sess)
	if _, err := protocol.ReadStreamHeader(&stream.output); err != nil {
		t.Fatal(err)
	}
	var hello protocol.DeviceHello
	if err := protocol.ReadJSON(&stream.output, &hello); err != nil {
		t.Fatal(err)
	}
	if !contains(hello.TransportCapabilities, protocol.CapabilitySpeedTest) {
		t.Fatal("Android did not advertise latency probe support")
	}
	if !contains(hello.TransportCapabilities, protocol.CapabilityE2EEV1) {
		t.Fatal("Android did not advertise E2EE v1 support")
	}
	if len(hello.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("Android DeviceHello E2EE identity key length = %d, want %d", len(hello.PublicKey), ed25519.PublicKeySize)
	}
}
func (s *androidProbeSession) Close() error                    { return nil }
func (s *androidProbeSession) Done() <-chan struct{}           { return nil }
func (s *androidProbeSession) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *androidProbeSession) RemoteAddr() net.Addr            { return nil }
func (s *androidProbeSession) LocalAddr() net.Addr             { return nil }

func newAndroidProbeClient(t *testing.T) (*Client, *androidProbeSession, *androidProbeStream) {
	t.Helper()
	stream := &androidProbeStream{}
	_ = protocol.WriteJSON(&stream.Buffer, protocol.SpeedTestResponse{RequestID: "probe_1", Success: true})
	_ = protocol.WriteJSON(&stream.Buffer, protocol.SpeedTestComplete{})
	sess := &androidProbeSession{stream: stream}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &Client{ctx: ctx, cancel: cancel, status: statusSnapshot{
		ConnectionState: "CONNECTED", ClientApproved: true,
		ProxyExits: []protocol.ProxyExit{{DeviceID: "online", Online: true}, {DeviceID: "offline", Online: false}},
	}}
	c.proxyDialer = agentclient.NewTunnelDialer(func() tunnel.TunnelSession { return sess }, nil)
	c.clientApproved.Store(true)
	return c, sess, stream
}

func TestProbeExitJSONRejectsUnavailableExitsBeforeOpeningStream(t *testing.T) {
	for _, name := range []string{"offline", "removed", "empty", "disconnected", "unauthorized", "stopped", "canceled", "busy"} {
		t.Run(name, func(t *testing.T) {
			c, sess, _ := newAndroidProbeClient(t)
			id := "online"
			switch name {
			case "offline", "removed":
				id = name
			case "empty":
				id = " "
			case "disconnected":
				c.status.ConnectionState = "DISCONNECTED"
			case "unauthorized":
				c.clientApproved.Store(false)
			case "stopped":
				c.closed = true
			case "canceled":
				c.cancel()
			case "busy":
				c.probeMu.Lock()
				defer c.probeMu.Unlock()
			}
			if raw, err := c.ProbeExitJSON(id); err == nil || raw != "" {
				t.Fatalf("unavailable exit succeeded: %q / %v", raw, err)
			}
			if sess.opens.Load() != 0 {
				t.Fatal("invalid probe opened a cached tunnel")
			}
		})
	}
	var absent *Client
	if _, err := absent.ProbeExitJSON("online"); err == nil {
		t.Fatal("nil core accepted a probe")
	}
}

func TestProbeExitJSONDiscardsResultsAfterPresenceOrConnectionChanges(t *testing.T) {
	for _, change := range []string{"offline", "removed", "reconnected", "inventory", "unauthorized"} {
		t.Run(change, func(t *testing.T) {
			c, _, stream := newAndroidProbeClient(t)
			stream.onHalfClose = func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				switch change {
				case "offline":
					c.status.ProxyExits[0].Online = false
				case "removed":
					c.status.ProxyExits = nil
				case "reconnected":
					c.probeEpoch++
				case "inventory":
					c.proxyExitRevision++
				case "unauthorized":
					c.clientApproved.Store(false)
				}
			}
			if raw, err := c.ProbeExitJSON("online"); err == nil || raw != "" {
				t.Fatalf("stale result published: %q / %v", raw, err)
			}
		})
	}
}

func TestProbeExitJSONReturnsRealResultForOnlineExit(t *testing.T) {
	c, sess, _ := newAndroidProbeClient(t)
	raw, err := c.ProbeExitJSON(" online ")
	if err != nil {
		t.Fatal(err)
	}
	var result agentclient.ExitProbeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result.ExitID != "online" || result.Path != "relay_tls" || result.LatencyMS <= 0 || sess.opens.Load() != 1 {
		t.Fatalf("invalid result: %s", raw)
	}
}
