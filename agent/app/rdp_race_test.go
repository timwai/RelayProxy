package app

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

type lateRDPConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *lateRDPConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func TestRDPRelayWinnerClosesLateDirectSuccess(t *testing.T) {
	direct, directPeer := net.Pipe()
	defer directPeer.Close()
	late := &lateRDPConn{Conn: direct, closed: make(chan struct{})}
	defer late.Close()
	relay, relayPeer := net.Pipe()
	defer relayPeer.Close()
	defer relay.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	winner, err := dialRDPTCPHappy(ctx, func(ctx context.Context) (net.Conn, error) {
		// A direct handshake can complete concurrently with cancellation. Its
		// success must not replace the accepted relay path or leak a socket.
		<-ctx.Done()
		return late, nil
	}, func(context.Context) (net.Conn, error) { return relay, nil })
	if err != nil || winner != relay {
		t.Fatalf("winner=%v error=%v, want relay", winner, err)
	}
	select {
	case <-late.closed:
	case <-time.After(time.Second):
		t.Fatal("discarded direct connection remained open")
	}
}
