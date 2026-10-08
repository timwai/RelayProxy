package p2p

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func TestCandidateUpdateArrivesBeforeControllerSession(t *testing.T) {
	manager := NewManager(context.Background(), nil, "", time.Minute, "")
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	newAddress := "198.51.100.20:55000"
	manager.HandleControl(protocol.RDPControlMessage{
		Type: protocol.RDPControlCandidateUpdate, SessionID: 81,
		SessionToken: token,
		Candidates: []protocol.RDPCandidate{{Protocol: "udp", Type: "reflexive", Address: newAddress}},
	})
	item := manager.newSession(81, "controller", "target", token,
		[]protocol.RDPCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.20:12345"}}, 0)
	if item == nil {
		t.Fatal("could not create controller session")
	}
	item.mu.Lock()
	got := append([]protocol.RDPCandidate(nil), item.candidates...)
	item.mu.Unlock()
	if len(got) != 1 || got[0].Address != newAddress {
		t.Fatalf("early candidate update was lost: %#v", got)
	}
}

func TestCandidateUpdateRejectsOtherSessionToken(t *testing.T) {
	manager := NewManager(context.Background(), nil, "", time.Minute, "")
	defer manager.Close()
	token := []byte("0123456789abcdef0123456789abcdef")
	item := manager.newSession(82, "controller", "target", token,
		[]protocol.RDPCandidate{{Protocol: "udp", Type: "lan", Address: "192.0.2.20:12345"}}, 0)
	manager.HandleControl(protocol.RDPControlMessage{
		Type: protocol.RDPControlCandidateUpdate, SessionID: 82,
		SessionToken: []byte("wrong-token-1234"),
		Candidates: []protocol.RDPCandidate{{Protocol: "udp", Type: "lan", Address: "198.51.100.20:4444"}},
	})
	item.mu.Lock()
	got := append([]protocol.RDPCandidate(nil), item.candidates...)
	item.mu.Unlock()
	if len(got) != 1 || got[0].Address != "192.0.2.20:12345" {
		t.Fatalf("unauthenticated candidate update replaced peer endpoints: %#v", got)
	}
}

// Exercise the actual target reader and controller dialer together: testing
// two generic punch clients alone misses mismatched handshake roles.
func newDirectUDPPair(t testing.TB) (*Session, *net.UDPConn) {
	t.Helper()
	local, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	send := func(context.Context, protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		return protocol.RDPControlMessage{Type: protocol.RDPControlLeaseAck}, nil
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	target := NewManager(context.Background(), send, local.LocalAddr().String(), time.Minute, "")
	t.Cleanup(func() { _ = target.Close() })
	item := target.newSession(42, "controller", "target", key, nil, 0)
	if err := target.startTargetUDP(item); err != nil {
		t.Fatal(err)
	}
	item.mu.Lock()
	port := item.udp.LocalAddr().(*net.UDPAddr).Port
	item.mu.Unlock()
	controller := NewManager(context.Background(), send, "", time.Minute, "")
	t.Cleanup(func() { _ = controller.Close() })
	session := controller.newSession(42, "controller", "target", key, []protocol.RDPCandidate{{
		Protocol: "udp", Type: "lan", Address: (&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}).String(),
	}}, 0)
	return session, local
}

func TestTargetUDPCompletesHandshakeAndForwardsTraffic(t *testing.T) {
	session, local := newDirectUDPPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	remote, err := session.DialUDP(ctx)
	if err != nil {
		t.Fatalf("RDP UDP handshake failed after %s: %v", time.Since(started), err)
	}
	defer remote.Close()
	t.Logf("RDP UDP direct handshake: %s", time.Since(started))
	// The winning connection must outlive the context used by the path race.
	cancel()
	if session.PathUDP() != "udp_p2p" {
		t.Fatalf("unexpected UDP path: %s", session.PathUDP())
	}
	deadline := time.Now().Add(2 * time.Second)
	_ = remote.SetDeadline(deadline)
	_ = local.SetDeadline(deadline)
	for _, payload := range [][]byte{[]byte("keyboard-input"), bytes.Repeat([]byte("screen-update"), 400)} {
		if _, err := remote.WriteTo(payload, nil); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(payload))
		n, source, err := local.ReadFromUDP(got)
		if err != nil || !bytes.Equal(got[:n], payload) {
			t.Fatalf("controller -> target: n=%d err=%v", n, err)
		}
		if _, err := local.WriteToUDP(payload, source); err != nil {
			t.Fatal(err)
		}
		n, _, err = remote.ReadFrom(got)
		if err != nil || !bytes.Equal(got[:n], payload) {
			t.Fatalf("target -> controller: n=%d err=%v", n, err)
		}
	}
}

func TestPublishCandidatesHonorsDialCancellation(t *testing.T) {
	started := make(chan struct{})
	m := NewManager(context.Background(), func(ctx context.Context, message protocol.RDPControlMessage) (protocol.RDPControlMessage, error) {
		close(started)
		<-ctx.Done()
		return protocol.RDPControlMessage{}, ctx.Err()
	}, "", time.Minute, "")
	defer m.Close()
	session := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		session.publishCandidates(ctx, "udp", []protocol.RDPCandidate{{
			Protocol: "udp", Type: "lan", Address: "192.0.2.1:1234",
		}})
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("candidate publication did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("losing direct dial kept waiting for candidate publication")
	}
}

func TestDialUDPCancelsBlockedRendezvousProbe(t *testing.T) {
	session, _ := newDirectUDPPair(t)
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	session.manager.rendezvous = probe.LocalAddr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := session.DialUDP(ctx)
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	_ = probe.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := probe.ReadFromUDP(make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled UDP dial returned: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled UDP dial waited for the rendezvous timeout")
	}
}

func TestCandidateWaitEndsWhenSessionCloses(t *testing.T) {
	m := NewManager(context.Background(), nil, "", time.Minute, "")
	defer m.Close()
	session := m.newSession(42, "controller", "target", []byte("0123456789abcdef"), nil, 0)
	done := make(chan struct{})
	go func() {
		session.candidateList(context.Background(), "udp")
		close(done)
	}()
	session.closeLocal()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closed session kept waiting for candidates")
	}
}

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
