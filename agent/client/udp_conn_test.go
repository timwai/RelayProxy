package client

import (
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestUDPTunnelConnRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	client := newUDPTunnelConn(c1, &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 53})
	errCh := make(chan error, 1)
	go func() {
		payload, err := protocol.ReadUDPDatagram(c2)
		if err != nil {
			errCh <- err
			return
		}
		errCh <- protocol.WriteUDPDatagram(c2, append([]byte("echo:"), payload...))
	}()

	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	n, err := client.WriteTo([]byte("ping"), nil)
	if err != nil || n != 4 {
		t.Fatalf("WriteTo: n=%d err=%v", n, err)
	}
	buf := make([]byte, 64)
	n, _, err = client.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Fatalf("got %q", buf[:n])
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}
