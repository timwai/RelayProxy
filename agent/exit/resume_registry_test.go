package exit

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	p2presume "relayproxy/internal/p2p/resume"
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
	rebound, err := registry.rebind(peer)
	if err != nil {
		t.Fatal(err)
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
		Type: p2presume.BindOpen, Identity: local.Identity, Generation: 5,
		SendOffset: 10, ReceiveOffset: 20,
	}
	if _, err := registry.rebind(stale); !errors.Is(err, p2presume.ErrBinding) {
		t.Fatalf("stale generation error=%v", err)
	}

	wrongIdentity, err := p2presume.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wrongToken := stale
	wrongToken.Generation = 6
	wrongToken.Identity.Token = wrongIdentity.Token
	if _, err := registry.rebind(wrongToken); !errors.Is(err, p2presume.ErrBinding) {
		t.Fatalf("wrong token error=%v", err)
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
