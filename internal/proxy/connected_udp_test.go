package proxy

import (
	"net"
	"testing"
	"time"
)

func connectedUDPFixture(t *testing.T) (net.PacketConn, *net.UDPConn) {
	t.Helper()
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	conn, err := net.DialUDP("udp4", nil, peer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	wrapped, err := WrapConnectedUDP(conn)
	if err != nil {
		t.Fatal(err)
	}
	_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
	_ = wrapped.SetDeadline(time.Now().Add(2 * time.Second))
	return wrapped, peer
}

func TestConnectedUDPWriteToFixedTargetAndReadReply(t *testing.T) {
	conn, peer := connectedUDPFixture(t)
	for _, target := range []net.Addr{peer.LocalAddr(), nil, &net.UDPAddr{IP: net.ParseIP("::ffff:127.0.0.1"), Port: peer.LocalAddr().(*net.UDPAddr).Port}} {
		if n, err := conn.WriteTo([]byte("ping"), target); err != nil || n != 4 {
			t.Fatalf("WriteTo(%v) = %d, %v", target, n, err)
		}
		buf := make([]byte, 64)
		n, source, err := peer.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "ping" {
			t.Fatalf("peer received %q, %v", buf[:n], err)
		}
		if _, err := peer.WriteTo([]byte("pong"), source); err != nil {
			t.Fatal(err)
		}
		n, source, err = conn.ReadFrom(buf)
		if err != nil || string(buf[:n]) != "pong" || source.String() != peer.LocalAddr().String() {
			t.Fatalf("ReadFrom = %q from %v, %v", buf[:n], source, err)
		}
	}
	if n, err := conn.WriteTo(nil, peer.LocalAddr()); err != nil || n != 0 {
		t.Fatalf("empty datagram = %d, %v", n, err)
	}
	if n, _, err := peer.ReadFrom(make([]byte, 1)); err != nil || n != 0 {
		t.Fatalf("empty datagram received = %d, %v", n, err)
	}
}

func TestConnectedUDPRejectsOtherDestinations(t *testing.T) {
	conn, peer := connectedUDPFixture(t)
	remote := peer.LocalAddr().(*net.UDPAddr)
	var typedNil *net.UDPAddr
	for _, target := range []net.Addr{
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 2), Port: remote.Port},
		&net.UDPAddr{IP: remote.IP, Port: 1},
		&net.UDPAddr{IP: remote.IP, Port: remote.Port + 65536},
		&net.UDPAddr{IP: remote.IP, Port: remote.Port, Zone: "different-zone"},
		&net.TCPAddr{IP: remote.IP, Port: remote.Port},
		typedNil,
	} {
		if n, err := conn.WriteTo([]byte("forbidden"), target); err == nil || n != 0 {
			t.Fatalf("WriteTo(%v) accepted another destination: %d, %v", target, n, err)
		}
	}
	_ = peer.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := peer.ReadFrom(make([]byte, 64)); err == nil {
		t.Fatal("rejected WriteTo sent a datagram")
	} else if e, ok := err.(net.Error); !ok || !e.Timeout() {
		t.Fatalf("unexpected receive error: %v", err)
	}
}

func TestConnectedUDPRequiresConnectedSocket(t *testing.T) {
	if _, err := WrapConnectedUDP(nil); err == nil {
		t.Fatal("nil UDP socket accepted")
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := WrapConnectedUDP(conn); err == nil {
		t.Fatal("unconnected UDP socket accepted")
	}
}
