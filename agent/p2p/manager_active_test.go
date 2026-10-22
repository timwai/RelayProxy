package p2p

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	directp2p "relayproxy/internal/p2p"
	"relayproxy/internal/p2p/secure"
	"relayproxy/internal/tunnel"
)

func TestPowerProfileAndSessionLimitPreserveActiveTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer clientUDP.Close()
	exitUDP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer exitUDP.Close()
	clientIdentity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	exitIdentity, err := secure.GenerateEphemeralIdentity()
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		session *directp2p.QUICSession
		err     error
	}
	accepted := make(chan result, 1)
	go func() {
		s, err := directp2p.AcceptQUIC(ctx, exitUDP, exitIdentity, clientIdentity.Fingerprint)
		accepted <- result{s, err}
	}()
	direct, err := directp2p.DialQUIC(ctx, clientUDP, exitUDP.LocalAddr().(*net.UDPAddr), clientIdentity, exitIdentity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	peer := <-accepted
	if peer.err != nil {
		t.Fatal(peer.err)
	}
	defer peer.session.Close()
	manager := NewManager(ctx, nil, nil, time.Minute)
	defer manager.Close()
	manager.maxExitSessions = 1
	key := []byte("0123456789abcdef0123456789abcdef")
	active := manager.newSession(1, "client", "active-exit", key, time.Now().Add(time.Minute).UnixMilli())
	active.mu.Lock()
	active.clientRole, active.state, active.direct = true, StateReady, direct
	active.mu.Unlock()
	active.lastUsed.Store(1)
	if _, ok := manager.PeekReadyForExit("active-exit"); !ok || active.lastUsed.Load() != 1 {
		t.Fatal("diagnostic lookup refreshed or lost the ready path")
	}
	if _, ok := manager.ReadyForExit("active-exit"); !ok || active.lastUsed.Load() <= 1 {
		t.Fatal("routing lookup did not refresh the ready path")
	}
	active.lastUsed.Store(1)
	stream, err := direct.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	idle := manager.newSession(2, "client", "idle-exit", key, time.Now().Add(time.Minute).UnixMilli())
	idle.mu.Lock()
	idle.clientRole, idle.state = true, StateReady
	idle.mu.Unlock()
	idle.lastUsed.Store(2)
	// A freshly established path may temporarily exceed the cache limit
	// while the existing path still carries an application stream.
	manager.enforceClientLimit(idle.ID)
	if _, ok := manager.Session(active.ID); !ok {
		t.Fatal("new path evicted the actively transferring path")
	}
	manager.SetPowerConstrained(true)
	if _, ok := manager.Session(active.ID); !ok {
		t.Fatal("screen-off/power-save transition evicted an active transfer")
	}
	if _, ok := manager.Session(idle.ID); ok {
		t.Fatal("idle path was retained instead of the active path")
	}
	if _, err := stream.Write([]byte("still transferring")); err != nil {
		t.Fatalf("power transition interrupted the stream: %v", err)
	}
	remote, err := peer.session.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	var payload [18]byte
	if _, err := io.ReadFull(remote, payload[:]); err != nil {
		t.Fatal(err)
	}
	if string(payload[:]) != "still transferring" {
		t.Fatalf("transfer corrupted: %q", payload)
	}
	if direct.ActiveStreams() != 1 || !tunnel.SupportsDatagrams(direct.QUICSession) {
		t.Fatal("power transition damaged the direct transport")
	}
}
