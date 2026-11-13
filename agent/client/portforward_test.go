package client

import (
	"context"
	"net"
	"testing"
	"time"

	"relayproxy/agent/portforward"
	"relayproxy/internal/protocol"
)

func TestPortForwardPathMapping(t *testing.T) {
	tests := []struct {
		input string
		want  portforward.Path
	}{
		{string(protocol.ProxyPathPublicDirectQUIC), portforward.PathPublicDirectQUIC},
		{string(protocol.ProxyPathP2PQUIC), portforward.PathP2PQUIC},
		{string(protocol.ProxyPathRelayQUIC), portforward.PathRelayQUIC},
		{string(protocol.ProxyPathRelayTLS), portforward.PathRelayTLS},
		{"", portforward.PathUnknown},
	}
	for _, test := range tests {
		if got := portForwardPathFromConn(testPathCarrier(test.input)); got != test.want {
			t.Fatalf("path %q: got %q want %q", test.input, got, test.want)
		}
	}
}

type testPathCarrier string

func (p testPathCarrier) ProxyPath() string { return string(p) }

func TestPacketDatagramSessionRoundTrip(t *testing.T) {
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, readErr := server.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			_, _ = server.WriteTo(buffer[:n], addr)
		}
	}()

	conn, err := net.Dial("udp", server.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	session := &packetDatagramSession{conn: &connectedPacketConn{Conn: conn}}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	payload := []byte("datagram-boundary")
	if err := session.Send(ctx, payload); err != nil {
		t.Fatal(err)
	}
	reply, err := session.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(reply) != string(payload) {
		t.Fatalf("got %q want %q", reply, payload)
	}
}

type connectedPacketConn struct {
	net.Conn
}

func (c *connectedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Read(p)
	return n, c.RemoteAddr(), err
}

func (c *connectedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Write(p)
}

func (c *connectedPacketConn) LocalAddr() net.Addr {
	return c.Conn.LocalAddr()
}

func (c *connectedPacketConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(t)
}

func (c *connectedPacketConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(t)
}

func (c *connectedPacketConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(t)
}
