package socks5

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"relayproxy/internal/proxy"
)

func TestConnectionLimitAndIdleReclaim(t *testing.T) {
	upstream, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	dial := testDialer(func(ctx context.Context, _ string, host string, port uint16) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	})
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: dial, MaxConnections: 1, IdleTimeout: 100 * time.Millisecond})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := proxyClient(t, s)
	negotiate(t, client)
	requestTarget(t, client, upstream.Addr().(*net.TCPAddr))
	_ = upstream.SetDeadline(time.Now().Add(time.Second))
	target, err := upstream.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var reply [10]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] != RepSuccess {
		t.Fatalf("reply=%v, %v", reply, err)
	}
	extra := proxyClient(t, s)
	assertDisconnected(t, extra)
	_ = client.(*net.TCPConn).CloseWrite()
	assertDisconnected(t, client)
	assertDisconnected(t, target)
	deadline := time.Now().Add(time.Second)
	for len(s.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.slots) != 0 {
		t.Fatal("idle session retained a connection slot")
	}
	second := proxyClient(t, s)
	negotiate(t, second)
}

type localUDPDialer struct{ testDialer }

func (localUDPDialer) DialUDP(ctx context.Context, _ string, host string, port uint16) (net.PacketConn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, err
	}
	return proxy.WrapConnectedUDP(conn.(*net.UDPConn))
}

func TestUDPAssociationIdleReclaimsControlSocketAndSlot(t *testing.T) {
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: testDialer(nil), MaxConnections: 1, UDPIdleTimeout: 100 * time.Millisecond})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, s)
	control, _ := openUDPAssociation(t, s)
	assertDisconnected(t, control)
	waitAssociationReleased(t, s)
	second := proxyClient(t, s)
	negotiate(t, second)
}

func TestUDPAssociationActivityRefreshesIdleInBothDirections(t *testing.T) {
	target, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: localUDPDialer{}, MaxConnections: 1, UDPIdleTimeout: 300 * time.Millisecond})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, s)
	control, addr := openUDPAssociation(t, s)
	client, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = target.SetDeadline(time.Now().Add(3 * time.Second))
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	packet, err := encodeUDPDatagram("127.0.0.1", uint16(target.LocalAddr().(*net.UDPAddr).Port), []byte("ping"))
	if err != nil {
		t.Fatal(err)
	}
	var buf [128]byte
	var relay *net.UDPAddr
	// Outbound-only traffic keeps an otherwise silent association alive.
	for range 5 {
		if _, err := client.Write(packet); err != nil {
			t.Fatal(err)
		}
		_, relay, err = target.ReadFromUDP(buf[:])
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Inbound-only traffic must refresh the same association clock.
	for range 5 {
		if _, err := target.WriteToUDP([]byte("pong"), relay); err != nil {
			t.Fatal(err)
		}
		n, err := client.Read(buf[:])
		if err != nil {
			t.Fatal(err)
		}
		_, _, payload, err := decodeUDPDatagram(buf[:n])
		if err != nil || string(payload) != "pong" {
			t.Fatalf("reply %q: %v", payload, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	assertDisconnected(t, control)
	waitAssociationReleased(t, s)
}

func openUDPAssociation(t *testing.T, s *Server) (net.Conn, *net.UDPAddr) {
	t.Helper()
	control := proxyClient(t, s)
	negotiate(t, control)
	if _, err := control.Write([]byte{Version5, CmdUDPAssociate, 0, AtypIPv4, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] != RepSuccess {
		t.Fatalf("UDP associate: %v, %v", reply, err)
	}
	return control, &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
}

func waitAssociationReleased(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(s.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	packets := len(s.packetConns)
	s.mu.Unlock()
	if len(s.slots) != 0 || packets != 0 {
		t.Fatalf("idle association retained slot=%d sockets=%d", len(s.slots), packets)
	}
}

func TestDialOutlivesHandshakeDeadline(t *testing.T) {
	upstream, peer := net.Pipe()
	defer peer.Close()
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", HandshakeTimeout: 50 * time.Millisecond, IdleTimeout: time.Second,
		Dialer: testDialer(func(context.Context, string, string, uint16) (net.Conn, error) {
			time.Sleep(150 * time.Millisecond)
			return upstream, nil
		}),
	})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	client := proxyClient(t, s)
	negotiate(t, client)
	requestTarget(t, client, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 80})
	var reply [10]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] != RepSuccess {
		t.Fatalf("reply=%v, %v", reply, err)
	}
}

func TestUDPReadFailureClosesControlAndReleasesHandler(t *testing.T) {
	s := startTestServer(t, testDialer(nil))
	client := proxyClient(t, s)
	negotiate(t, client)
	request := []byte{Version5, CmdUDPAssociate, 0, AtypIPv4, 0, 0, 0, 0}
	request = binary.BigEndian.AppendUint16(request, 0)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(client, reply[:]); err != nil || reply[1] != RepSuccess {
		t.Fatalf("reply=%v, %v", reply, err)
	}
	s.mu.Lock()
	for conn := range s.packetConns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	assertDisconnected(t, client)
	deadline := time.Now().Add(time.Second)
	for len(s.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.slots) != 0 {
		t.Fatal("UDP handler remained blocked on the control reader")
	}
}
