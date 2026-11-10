package app

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type lateRDPConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

type rdpControlTestStream struct{ net.Conn }

func (s *rdpControlTestStream) CloseWrite() error { return s.Close() }

type rdpControlTestSession struct {
	tunnel.TunnelSession
	stream tunnel.TunnelStream
}

func (s *rdpControlTestSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return s.stream, nil
}

func TestRDPControlCancellationInterruptsPendingIO(t *testing.T) {
	for _, stage := range []string{"write", "read"} {
		t.Run(stage, func(t *testing.T) {
			local, peer := net.Pipe()
			defer local.Close()
			defer peer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := (&Agent{}).sendRDPControlRequest(ctx, &rdpControlTestSession{stream: &rdpControlTestStream{local}}, protocol.RDPControlMessage{Type: protocol.RDPControlCandidateUpdate})
				done <- err
			}()
			if stage == "read" {
				peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := protocol.ReadStreamHeader(peer); err != nil {
					t.Fatal(err)
				}
				var msg protocol.RDPControlMessage
				if err := protocol.ReadJSON(peer, &msg); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled control request succeeded")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("cancelled discovery kept control I/O alive")
			}
		})
	}
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
