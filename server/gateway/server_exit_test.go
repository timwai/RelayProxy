package gateway

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	agentexit "relayproxy/agent/exit"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"relayproxy/server/session"
)

func newServerExitTestHandler(t *testing.T) (*agentexit.Handler, *acl.Checker) {
	t.Helper()
	policy := acl.Policy{
		AllowInternet: true, AllowLoopback: true,
		Rules: []acl.Rule{}, AccessHosts: []string{}, AccessCIDRs: []string{},
	}
	local, err := acl.NewChecker(policy)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := acl.NewChecker(policy)
	if err != nil {
		t.Fatal(err)
	}
	handler := agentexit.NewHandler(agentexit.HandlerConfig{ACLChecker: local, ConnectTimeout: time.Second})
	t.Cleanup(func() { _ = handler.Close() })
	return handler, relay
}

func TestServerExitRoutesTCPWithoutDeviceSession(t *testing.T) {
	handler, relay := newServerExitTestHandler(t)
	router := NewStreamRouter(session.NewManager(), relay, nil, nil)
	router.SetLocalExit(handler)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		router.HandleClientStream(ctx, &policyTestStream{left}, &session.DeviceSession{
			DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
		})
		close(done)
	}()

	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	if err := protocol.WriteStreamHeader(right, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenTCP,
		RequestID: "server-exit-tcp", ExitDeviceID: protocol.ServerExitDeviceID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(right, protocol.OpenTCPRequest{
		RequestID: "server-exit-tcp", Host: "127.0.0.1", Port: port, TimeoutMs: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(right, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success {
		t.Fatalf("server exit failed: %+v", response)
	}
	if _, err := right.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(right, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo=%q, want ping", buf)
	}

	_ = right.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server-exit TCP stream did not stop")
	}
}

func TestServerExitRoutesUDPWithStreamFallback(t *testing.T) {
	handler, relay := newServerExitTestHandler(t)
	router := NewStreamRouter(session.NewManager(), relay, nil, nil)
	router.SetLocalExit(handler)

	udpServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpServer.Close()
	go func() {
		buf := make([]byte, 2048)
		n, peer, err := udpServer.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = udpServer.WriteToUDP(buf[:n], peer)
	}()

	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		router.HandleClientStream(ctx, &policyTestStream{left}, &session.DeviceSession{
			DeviceID: "client", Grants: []string{protocol.CapabilityProxyClient},
		})
		close(done)
	}()

	port := uint16(udpServer.LocalAddr().(*net.UDPAddr).Port)
	_ = right.SetDeadline(time.Now().Add(3 * time.Second))
	if err := protocol.WriteStreamHeader(right, &protocol.StreamHeader{
		Magic: protocol.MagicHeader, Version: protocol.CurrentVersion, Type: protocol.FrameTypeOpenUDP,
		RequestID: "server-exit-udp", ExitDeviceID: protocol.ServerExitDeviceID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(right, protocol.OpenUDPRequest{
		RequestID: "server-exit-udp", Host: "127.0.0.1", Port: port, TimeoutMs: 1000,
		Mode: protocol.UDPModeDatagram,
	}); err != nil {
		t.Fatal(err)
	}
	var response protocol.OpenUDPResponse
	if err := protocol.ReadJSON(right, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Success || response.Mode != protocol.UDPModeStream || response.AssociationID != 0 {
		t.Fatalf("unexpected server UDP negotiation: %+v", response)
	}

	packetConn := tunnel.NewUDPStreamConn(&policyTestStream{right}, udpServer.LocalAddr())
	if _, err := packetConn.WriteTo([]byte("ping"), nil); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	n, _, err := packetConn.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "ping" {
		t.Fatalf("UDP echo=%q, want ping", buf[:n])
	}
	_ = packetConn.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server-exit UDP stream did not stop")
	}
}

func TestServerExitParticipatesInAutoSelectionAmbiguity(t *testing.T) {
	handler, relay := newServerExitTestHandler(t)
	manager := session.NewManager()
	manager.Register(&session.DeviceSession{
		DeviceID: "device-exit", OwnerUserID: "owner", Grants: []string{protocol.CapabilityProxyExit},
	})
	router := NewStreamRouter(manager, relay, nil, nil)
	router.SetLocalExit(handler)
	client := &session.DeviceSession{DeviceID: "client", OwnerUserID: "owner"}

	if _, _, err := router.resolveExitSession(client, ""); err != errMultipleExits {
		t.Fatalf("auto-select error=%v, want %v", err, errMultipleExits)
	}
	exitSession, local, err := router.resolveExitSession(client, protocol.ServerExitDeviceID)
	if err != nil || !local || exitSession != nil {
		t.Fatalf("explicit server exit resolved session=%v local=%v err=%v", exitSession, local, err)
	}
}
