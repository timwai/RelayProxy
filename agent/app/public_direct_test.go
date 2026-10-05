package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	proxydirect "relayproxy/agent/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type publicDirectLifecycleSession struct {
	once sync.Once
	done chan struct{}
}

func newPublicDirectLifecycleSession() *publicDirectLifecycleSession {
	return &publicDirectLifecycleSession{done: make(chan struct{})}
}

func (s *publicDirectLifecycleSession) OpenStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *publicDirectLifecycleSession) AcceptStream(context.Context) (tunnel.TunnelStream, error) {
	return nil, errors.New("not supported")
}

func (s *publicDirectLifecycleSession) Transport() tunnel.TransportType { return tunnel.TransportQUIC }
func (s *publicDirectLifecycleSession) RemoteAddr() net.Addr            { return &net.UDPAddr{} }
func (s *publicDirectLifecycleSession) LocalAddr() net.Addr             { return &net.UDPAddr{} }

func (s *publicDirectLifecycleSession) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *publicDirectLifecycleSession) Done() <-chan struct{} { return s.done }

func TestClearPublicDirectStateClosesSessionsForEndedRelayGeneration(t *testing.T) {
	directSession := newPublicDirectLifecycleSession()
	manager := proxydirect.NewClientManager(context.Background(), func() string { return "client" }, proxydirect.ClientManagerOptions{
		AttemptTimeout: time.Second,
		Dial: func(context.Context, proxydirect.DialConfig) (tunnel.TunnelSession, error) {
			return directSession, nil
		},
	})
	defer manager.Close()

	fingerprint := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	manager.UpdateInventory([]protocol.ProxyExit{{
		DeviceID: "exit", Online: true,
		Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{
			Available:       true,
			Transport:       "quic",
			Ticket:          []byte("relay-generation-ticket"),
			TicketExpiresAt: time.Now().Add(time.Minute).Unix(),
			Endpoints: []protocol.PublicDirectEndpoint{{
				Protocol:        protocol.PublicDirectEndpointProtocolUDP,
				Address:         "203.0.113.20:35820",
				Source:          protocol.PublicDirectEndpointObserved,
				Verified:        true,
				CertFingerprint: fingerprint,
			}},
		}},
	}})
	if !manager.EnsureClient("exit") {
		t.Fatal("public direct attempt was not started")
	}
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := manager.ReadyForExit("exit"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("public direct session did not become ready")
		}
		time.Sleep(time.Millisecond)
	}

	relay := newPublicDirectLifecycleSession()
	agent := &Agent{epoch: 7, readySession: relay, proxyDirect: manager}
	agent.clearPublicDirectState(relay, 7)

	select {
	case <-directSession.Done():
	case <-time.After(time.Second):
		t.Fatal("ended relay generation did not close public direct session")
	}
	if _, ok := manager.ReadyForExit("exit"); ok {
		t.Fatal("ended relay generation left public direct session selectable")
	}
}

func TestClearPublicDirectStateDoesNotClearNewerRelayGeneration(t *testing.T) {
	manager := proxydirect.NewClientManager(context.Background(), func() string { return "client" }, proxydirect.ClientManagerOptions{})
	defer manager.Close()
	oldRelay := newPublicDirectLifecycleSession()
	newRelay := newPublicDirectLifecycleSession()
	agent := &Agent{epoch: 8, readySession: newRelay, proxyDirect: manager}

	agent.clearPublicDirectState(oldRelay, 7)

	agent.mu.RLock()
	stillCurrent := agent.epoch == 8 && agent.readySession == newRelay
	agent.mu.RUnlock()
	if !stillCurrent {
		t.Fatal("stale relay cleanup modified the current relay generation")
	}
}
