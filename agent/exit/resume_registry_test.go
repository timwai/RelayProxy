package exit

import (
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	p2presume "relayproxy/internal/p2p/resume"
	"relayproxy/internal/tunnel"
)

type trackedConn struct {
	net.Conn
	closes atomic.Int32
}

func (c *trackedConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func newResumeBinding(t *testing.T, kind p2presume.BindType, generation, sent, received uint64) p2presume.Binding {
	t.Helper()
	identity, err := p2presume.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return p2presume.Binding{
		Type:          kind,
		Identity:      identity,
		Generation:    generation,
		SendOffset:    sent,
		ReceiveOffset: received,
	}
}

func TestResumeRegistryRetainsTargetAcrossDetachAndRebind(t *testing.T) {
	registry := newResumeRegistry(80*time.Millisecond, 4)
	a, b := net.Pipe()
	defer b.Close()
	target := &trackedConn{Conn: a}

	local := newResumeBinding(t, p2presume.BindAck, 1, 100, 80)
	session, err := registry.register(local, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.detach(local); err != nil {
		t.Fatal(err)
	}

	peer := p2presume.Binding{
		Type:          p2presume.BindOpen,
		Identity:      local.Identity,
		Generation:    2,
		SendOffset:    80,
		ReceiveOffset: 100,
	}
	rebound, retry, err := registry.rebind(peer)
	if err != nil {
		t.Fatal(err)
	}
	if retry {
		t.Fatal("new generation was classified as a retry")
	}
	if rebound != session || session.currentGeneration() != 2 {
		t.Fatal("rebind did not preserve logical target session")
	}

	time.Sleep(120 * time.Millisecond)
	if target.closes.Load() != 0 {
		t.Fatal("old detach timer closed a successfully rebound target")
	}
	conn, err := session.targetConn()
	if err != nil || conn != target {
		t.Fatalf("target after rebind conn=%v err=%v", conn, err)
	}

	registry.closeAll()
	if target.closes.Load() != 1 {
		t.Fatalf("target close count=%d, want 1", target.closes.Load())
	}
}

func TestResumeRegistryRejectsStaleAndWrongTokenRebind(t *testing.T) {
	registry := newResumeRegistry(time.Second, 4)
	defer registry.closeAll()
	a, b := net.Pipe()
	defer b.Close()
	local := newResumeBinding(t, p2presume.BindAck, 5, 20, 10)
	session, err := registry.register(local, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.detach(local); err != nil {
		t.Fatal(err)
	}

	stale := p2presume.Binding{
		Type: p2presume.BindOpen, Identity: local.Identity, Generation: 4,
		SendOffset: 10, ReceiveOffset: 20,
	}
	if _, _, err := registry.rebind(stale); !errors.Is(err, p2presume.ErrBinding) {
		t.Fatalf("stale generation error=%v", err)
	}

	wrongIdentity, err := p2presume.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wrongToken := stale
	wrongToken.Generation = 6
	wrongToken.Identity.Token = wrongIdentity.Token
	if _, _, err := registry.rebind(wrongToken); !errors.Is(err, p2presume.ErrBinding) {
		t.Fatalf("wrong token error=%v", err)
	}
}

func TestResumeRegistryRetriesDetachedCurrentGeneration(t *testing.T) {
	registry := newResumeRegistry(time.Second, 4)
	defer registry.closeAll()
	a, b := net.Pipe()
	defer b.Close()
	local := newResumeBinding(t, p2presume.BindAck, 1, 100, 80)
	session, err := registry.register(local, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.detach(local); err != nil {
		t.Fatal(err)
	}

	peer := p2presume.Binding{
		Type: p2presume.BindOpen, Identity: local.Identity, Generation: 2,
		SendOffset: 80, ReceiveOffset: 100,
	}
	if _, retry, err := registry.rebind(peer); err != nil || retry {
		t.Fatalf("initial rebind retry=%v err=%v", retry, err)
	}
	if _, _, err := registry.rebind(peer); !errors.Is(err, p2presume.ErrBinding) {
		t.Fatalf("active current-generation retry error=%v", err)
	}

	local.Generation = 2
	if err := session.detach(local); err != nil {
		t.Fatal(err)
	}
	rebound, retry, err := registry.rebind(peer)
	if err != nil {
		t.Fatal(err)
	}
	if rebound != session || !retry || session.currentGeneration() != 2 {
		t.Fatalf("detached retry session=%p want=%p retry=%v generation=%d",
			rebound, session, retry, session.currentGeneration())
	}
}

func TestLogicalResumeRetryRebindsDetachedCurrentGeneration(t *testing.T) {
	registry := newResumeRegistry(time.Second, 4)
	defer registry.closeAll()
	targetExit, targetPeer := net.Pipe()
	defer targetPeer.Close()

	identity, err := p2presume.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	local := p2presume.Binding{Type: p2presume.BindAck, Identity: identity, Generation: 1}
	session, err := registry.registerLogical(local, targetExit, "target", 1024, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	client1, exit1 := net.Pipe()
	done1, err := session.bindTransport(pipeTunnelStream{Conn: exit1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = client1.Close()
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("initial generation did not detach")
	}
	waitResumeSessionDetached(t, session)

	peer := p2presume.Binding{Type: p2presume.BindOpen, Identity: identity, Generation: 2}
	if _, retry, err := registry.rebind(peer); err != nil || retry {
		t.Fatalf("new generation retry=%v err=%v", retry, err)
	}
	client2, exit2 := net.Pipe()
	done2, err := session.bindTransport(pipeTunnelStream{Conn: exit2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	_ = client2.Close()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("committed rebind generation did not detach")
	}
	waitResumeSessionDetached(t, session)

	if _, retry, err := registry.rebind(peer); err != nil || !retry {
		t.Fatalf("detached generation retry=%v err=%v", retry, err)
	}
	clientRetry, exitRetry := net.Pipe()
	doneRetry, err := session.retryTransport(pipeTunnelStream{Conn: exitRetry}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if session.currentGeneration() != 2 {
		t.Fatalf("retry advanced generation to %d", session.currentGeneration())
	}
	_ = clientRetry.Close()
	select {
	case <-doneRetry:
	case <-time.After(time.Second):
		t.Fatal("retried generation did not detach")
	}
}

func waitResumeSessionDetached(t *testing.T, session *resumeTargetSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		detached := !session.detachedAt.IsZero()
		session.mu.Unlock()
		if detached {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("resume session did not enter recovery grace")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestResumeRegistryExpiresDetachedTarget(t *testing.T) {
	registry := newResumeRegistry(25*time.Millisecond, 2)
	a, b := net.Pipe()
	defer b.Close()
	target := &trackedConn{Conn: a}
	local := newResumeBinding(t, p2presume.BindAck, 1, 0, 0)
	session, err := registry.register(local, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.detach(local); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for registry.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if registry.len() != 0 {
		t.Fatal("detached session was not expired")
	}
	if target.closes.Load() != 1 {
		t.Fatalf("expired target close count=%d, want 1", target.closes.Load())
	}
}

func TestResumeRegistryCapacityAndDuplicateProtection(t *testing.T) {
	registry := newResumeRegistry(time.Second, 1)
	defer registry.closeAll()
	a1, b1 := net.Pipe()
	defer b1.Close()
	local := newResumeBinding(t, p2presume.BindAck, 1, 0, 0)
	if _, err := registry.register(local, a1); err != nil {
		t.Fatal(err)
	}

	a2, b2 := net.Pipe()
	defer a2.Close()
	defer b2.Close()
	if _, err := registry.register(local, a2); !errors.Is(err, errResumeSessionExists) {
		t.Fatalf("duplicate register error=%v", err)
	}

	other := newResumeBinding(t, p2presume.BindAck, 1, 0, 0)
	if _, err := registry.register(other, a2); !errors.Is(err, errResumeSessionCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
}

func TestLogicalResumeTargetSurvivesTransportRebind(t *testing.T) {
	registry := newResumeRegistry(80*time.Millisecond, 4)
	targetExit, targetPeer := net.Pipe()
	defer targetPeer.Close()

	identity, err := p2presume.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	local := p2presume.Binding{
		Type:       p2presume.BindAck,
		Identity:   identity,
		Generation: 1,
	}
	var doneCalls atomic.Int32
	metrics := &tunnel.PipeMetrics{}
	session, err := registry.registerLogical(local, targetExit, "target", 1024, metrics, func() {
		doneCalls.Add(1)
	})
	if err != nil {
		t.Fatal(err)
	}

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		buf := make([]byte, 64)
		for {
			n, err := targetPeer.Read(buf)
			if n > 0 {
				if _, writeErr := targetPeer.Write(buf[:n]); writeErr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	clientState, err := p2presume.NewStreamState(identity, 1024)
	if err != nil {
		t.Fatal(err)
	}
	client, err := p2presume.NewEndpoint(clientState)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	clientTransport1, exitTransport1 := net.Pipe()
	done1, err := session.bindTransport(pipeTunnelStream{Conn: exitTransport1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Bind(clientTransport1, 1); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("first"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "first" {
		t.Fatalf("first echo=%q", buf)
	}

	_ = clientTransport1.Close()
	select {
	case <-done1:
	case <-time.After(time.Second):
		t.Fatal("first transport generation did not end")
	}

	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		detached := !session.detachedAt.IsZero()
		session.mu.Unlock()
		if detached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Exit session did not enter recovery grace")
		}
		time.Sleep(time.Millisecond)
	}

	peerRebind, err := clientState.Binding(p2presume.BindOpen, 2)
	if err != nil {
		t.Fatal(err)
	}
	rebound, retry, err := registry.rebind(peerRebind)
	if err != nil {
		t.Fatal(err)
	}
	if retry {
		t.Fatal("new logical generation was classified as a retry")
	}
	if rebound != session {
		t.Fatal("rebind replaced logical target session")
	}
	exitBinding, err := session.localBinding()
	if err != nil {
		t.Fatal(err)
	}
	clientLocal, err := clientState.Binding(p2presume.BindAck, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := p2presume.ValidateRebindResponse(clientLocal, exitBinding); err != nil {
		t.Fatalf("client rejected Exit rebind offsets: %v", err)
	}

	clientTransport2, exitTransport2 := net.Pipe()
	if _, err := session.bindTransport(pipeTunnelStream{Conn: exitTransport2}, 2); err != nil {
		t.Fatal(err)
	}
	if err := client.Bind(clientTransport2, 2); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	buf = make([]byte, len("second"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "second" {
		t.Fatalf("second echo=%q", buf)
	}
	pipe := metrics.Snapshot()
	if pipe.Up.ReadBytes < uint64(len("firstsecond")) || pipe.Down.ReadBytes < uint64(len("firstsecond")) {
		t.Fatalf("logical bridge metrics missed resumed traffic: %+v", pipe)
	}

	// Closing only the client transport is recoverable; the target remains
	// registered until the grace period expires.
	_ = client.Close()
	deadline = time.Now().Add(time.Second)
	for registry.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if registry.len() != 0 {
		t.Fatal("logical target was not released after recovery grace")
	}
	select {
	case <-echoDone:
	case <-time.After(time.Second):
		t.Fatal("target socket remained open after session expiry")
	}
	if doneCalls.Load() != 1 {
		t.Fatalf("logical session completion callbacks=%d, want 1", doneCalls.Load())
	}
}
