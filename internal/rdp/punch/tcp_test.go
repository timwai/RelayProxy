package punch

import (
	"context"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestTCPPunchAuthenticatesSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	key := []byte("01234567890123456789012345678901")
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		accepted, id, err := Accept(context.Background(), conn, func(sessionID uint64) ([]byte, bool) {
			return key, sessionID == 99
		}, time.Second)
		if accepted != nil {
			_ = accepted.Close()
		}
		if err == nil && id != 99 {
			done <- net.ErrClosed
			return
		}
		done <- err
	}()
	conn, err := Dial(context.Background(), []protocol.RDPCandidate{{Protocol: "tcp", Type: "lan", Address: listener.Addr().String()}}, 99, key, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
