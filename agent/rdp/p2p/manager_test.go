package p2p

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestStartTargetUDPCloseRaceDoesNotPublishSockets(t *testing.T) {
	m := NewManager(context.Background(), func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}, "127.0.0.1:9", time.Minute, "")
	defer m.Close()

	for i := uint64(1); i <= 32; i++ {
		item := m.newSession(i, "controller", "target", []byte("0123456789abcdef"), nil, 0)
		if item == nil {
			t.Fatal("session was not created")
		}
		started := make(chan error, 1)
		go func() { started <- m.startTargetUDP(item) }()
		item.closeLocal()
		err := <-started
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("target UDP startup failed unexpectedly: %v", err)
		}
		item.mu.Lock()
		udp, local := item.udp, item.localUDP
		item.mu.Unlock()
		if udp != nil || local != nil {
			t.Fatalf("closed session retained UDP sockets: udp=%v local=%v", udp, local)
		}
	}
}
