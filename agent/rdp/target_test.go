package rdp

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type testPacket struct {
	payload []byte
	addr    net.Addr
}

type testPacketConn struct {
	in   chan testPacket
	out  chan []byte
	done chan struct{}
	once sync.Once
}

func newTestPacketConn() *testPacketConn {
	return &testPacketConn{
		in:   make(chan testPacket, 2),
		out:  make(chan []byte, 2),
		done: make(chan struct{}),
	}
}

func (c *testPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case item := <-c.in:
		n := copy(p, item.payload)
		return n, item.addr, nil
	case <-c.done:
		return 0, nil, net.ErrClosed
	}
}

func (c *testPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	payload := append([]byte(nil), p...)
	select {
	case c.out <- payload:
		return len(p), nil
	case <-c.done:
		return 0, net.ErrClosed
	}
}

func (c *testPacketConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *testPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (c *testPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *testPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *testPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestBridgePacketWritesToConnectedLocalUDP(t *testing.T) {
	target, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	local, err := net.DialUDP("udp4", nil, target.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}

	remote := newTestPacketConn()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridgePacket(ctx, local, remote) }()

	payload := []byte("rdp udp syn")
	remote.in <- testPacket{
		payload: payload,
		addr:    &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 3389},
	}

	if err := target.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	n, source, err := target.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("target did not receive controller UDP packet: %v", err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("target received %q, want %q", buffer[:n], payload)
	}

	reply := []byte("rdp udp syn-ack")
	if _, err := target.WriteToUDP(reply, source); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-remote.out:
		if !bytes.Equal(got, reply) {
			t.Fatalf("controller received %q, want %q", got, reply)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not receive target UDP response")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bridgePacket did not stop after context cancellation")
	}
}
