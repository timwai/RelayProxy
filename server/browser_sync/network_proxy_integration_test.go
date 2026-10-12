package browsersync

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

type browserEchoStream struct{ net.Conn }

func (s *browserEchoStream) CloseWrite() error { return s.Close() }

type browserEchoTunnel struct {
	requests chan protocol.OpenTCPRequest
	frames   chan protocol.FrameType
}

func (s *browserEchoTunnel) OpenStream(ctx context.Context) (tunnel.TunnelStream, error) {
	client, agent := net.Pipe()
	go func() {
		defer agent.Close()
		header, err := protocol.ReadStreamHeader(agent)
		if err != nil || (header.Type != protocol.FrameTypeBrowserNetworkTCP &&
			header.Type != protocol.FrameTypeOpenTCP) {
			return
		}
		if s.frames != nil {
			s.frames <- header.Type
		}
		var req protocol.OpenTCPRequest
		if err := protocol.ReadJSON(agent, &req); err != nil {
			return
		}
		s.requests <- req
		if err := protocol.WriteJSON(agent, protocol.OpenTCPResponse{
			RequestID: req.RequestID, Success: true,
		}); err != nil {
			return
		}
		_, _ = io.Copy(agent, agent)
	}()
	return &browserEchoStream{Conn: client}, nil
}

func (s *browserEchoTunnel) AcceptStream(ctx context.Context) (tunnel.TunnelStream, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *browserEchoTunnel) Transport() tunnel.TransportType { return tunnel.TransportTLS }
func (s *browserEchoTunnel) RemoteAddr() net.Addr            { return &net.TCPAddr{} }
func (s *browserEchoTunnel) LocalAddr() net.Addr             { return &net.TCPAddr{} }
func (s *browserEchoTunnel) Close() error                    { return nil }
func (s *browserEchoTunnel) Done() <-chan struct{}           { return nil }

func TestNetworkProxyCONNECTRelaysBytesThroughApprovedAgent(t *testing.T) {
	store := testBrowserStore(t)
	source, receiver, ruleID := approvedDeliveryRule(t, store)
	ctx := context.Background()
	const exitID = "dev_browser_test_exit_01"
	if err := store.OfferNetworkBinding(ctx, source.ID, ruleID, "login.example.com", exitID); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptNetworkBinding(ctx, receiver.ID, ruleID, "login.example.com"); err != nil {
		t.Fatal(err)
	}

	sessions := session.NewManager()
	echo := &browserEchoTunnel{requests: make(chan protocol.OpenTCPRequest, 1),frames: make(chan protocol.FrameType, 1)}
	sessions.Register(&session.DeviceSession{
		DeviceID: exitID, IdentityID: "id_1", DeviceName: "Approved Agent",
		Grants:       []string{protocol.CapabilityProxyExit},
		Capabilities: []string{protocol.CapabilityBrowserNetworkExit}, Tunnel: echo,
	})
	t.Cleanup(func() { sessions.Unregister(exitID) })
	proxy := NewNetworkProxy(store, sessions)
	// Browser network bindings may use any same-identity Agent with an
	// approved proxy.exit grant. The remote Agent need not have local
	// browser-sharing enabled; A and B consent to the binding separately.
	identity, err := store.deviceAllowed(ctx, receiver.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !proxy.AllowedExit(identity.IdentityID, exitID) {
		t.Fatal("same-Identity proxy.exit Agent rejected")
	}
	exitSession, _ := sessions.Get(exitID)
	exitSession.Capabilities = nil
	if !proxy.AllowedExit(identity.IdentityID, exitID) {
		t.Fatal("remote proxy.exit Agent hidden due to missing local browser-sharing flag")
	}
	if got := proxy.AvailableExits(ctx, source.ID); len(got) != 1 || got[0]["id"] != exitID {
		t.Fatalf("approved remote exit not listed: %v", got)
	}
	exitSession.Capabilities = []string{protocol.CapabilityBrowserNetworkExit}
	exitSession.IdentityID = "id_other"
	if proxy.AllowedExit(identity.IdentityID, exitID) {
		t.Fatal("browser could use a shared Exit under another Identity")
	}
	exitSession.IdentityID = identity.IdentityID
	exitSession.Grants = nil
	if proxy.AllowedExit(identity.IdentityID, exitID) {
		t.Fatal("browser could use an Exit without proxy.exit grant")
	}
	exitSession.Grants = []string{protocol.CapabilityProxyExit}
	// Exercise the actual CONNECT through a remote Agent that does not
	// advertise local Browser Network sharing. This must use the regular
	// ACL-protected TCP egress frame, rather than a frame the Agent rejects.
	exitSession.Capabilities = nil
	lease, err := proxy.Issue(ctx, receiver.ID)
	if err != nil {
		t.Fatal(err)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte("browser:" + lease["token"].(string)))

	httpsServer := httptest.NewTLSServer(proxy)
	defer httpsServer.Close()
	addr := strings.TrimPrefix(httpsServer.URL, "https://")
	tlsConfig := httpsServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	tlsConfig.NextProtos = []string{"http/1.1"}
	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(conn, "CONNECT login.example.com:443 HTTP/1.1\r\nHost: login.example.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", credentials); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "200 Connection Established") {
		t.Fatalf("CONNECT status %q: %v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	select {
	case frameType := <-echo.frames:
		if frameType != protocol.FrameTypeOpenTCP {
			t.Fatalf("remote authorized Agent without opt-in received unexpected frame %v", frameType)
		}
	case <-time.After(3*time.Second):
		t.Fatal("remote Agent never received standard proxy.exit TCP frame")
	}
	select {
	case request := <-echo.requests:
		if request.Host != "login.example.com" || request.Port != 443 {
			t.Fatalf("wrong agent target: %+v", request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Agent never received OpenTCP request")
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("Relay did not forward Agent response: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("unexpected tunneled payload %q", buf)
	}
}

func TestNetworkProxyRevocationClosesEstablishedCONNECT(t *testing.T) {
	store := testBrowserStore(t)
	source, receiver, ruleID := approvedDeliveryRule(t, store)
	ctx := context.Background()
	const exitID = "dev_revoke_stream"
	if err := store.OfferNetworkBinding(ctx, source.ID, ruleID, "login.example.com", exitID); err != nil {
		t.Fatal(err)
	}
	if err := store.AcceptNetworkBinding(ctx, receiver.ID, ruleID, "login.example.com"); err != nil {
		t.Fatal(err)
	}
	sessions := session.NewManager()
	echo := &browserEchoTunnel{requests: make(chan protocol.OpenTCPRequest, 1)}
	sessions.Register(&session.DeviceSession{
		DeviceID: exitID, IdentityID: "id_1", DeviceName: "Exit",
		Grants:       []string{protocol.CapabilityProxyExit},
		Capabilities: []string{protocol.CapabilityBrowserNetworkExit}, Tunnel: echo,
	})
	t.Cleanup(func() { sessions.Unregister(exitID) })
	proxy := NewNetworkProxy(store, sessions)
	lease, err := proxy.Issue(ctx, receiver.ID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(proxy)
	defer server.Close()
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.NextProtos = []string{"http/1.1"}
	conn, err := tls.Dial("tcp", strings.TrimPrefix(server.URL, "https://"), config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(9 * time.Second))
	credentials := base64.StdEncoding.EncodeToString([]byte("browser:" + lease["token"].(string)))
	if _, err := fmt.Fprintf(conn, "CONNECT login.example.com:443 HTTP/1.1\r\nHost: login.example.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", credentials); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "200 Connection Established") {
		t.Fatalf("cannot establish CONNECT stream: %q %v", line, err)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if err := store.RevokeNetworkBinding(ctx, receiver.ID, ruleID); err != nil {
		t.Fatal(err)
	}
	// The CONNECT must close without waiting 15 minutes for its token TTL.
	if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("revoked CONNECT remained active or timed out: %v", err)
	}
}

func TestNetworkProxyCloseSignalsActiveWatchers(t *testing.T) {
	proxy := NewNetworkProxy(nil, nil)
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-proxy.shutdown:
	default:
		t.Fatal("shutdown did not wake hijacked CONNECT watchers")
	}
	if err := proxy.Close(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown failed: %v", err)
	}
}

func TestBrowserExitStreamTypePreservesLocalOptInAndRemoteFallback(t *testing.T) {
	local := &session.DeviceSession{
		Capabilities: []string{protocol.CapabilityBrowserNetworkExit},
	}
	if got := browserExitStreamType(local); got != protocol.FrameTypeBrowserNetworkTCP {
		t.Fatalf("opted-in local Agent lost Browser Network frame: %v", got)
	}
	remote := &session.DeviceSession{}
	if got := browserExitStreamType(remote); got != protocol.FrameTypeOpenTCP {
		t.Fatalf("remote proxy.exit Agent requires ordinary egress frame: %v", got)
	}
}
