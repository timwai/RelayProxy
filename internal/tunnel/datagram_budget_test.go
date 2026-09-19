package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"relayproxy/internal/protocol"
)

func budgetMux(t *testing.T, budget *datagramBudget) *datagramMux {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m := &datagramMux{ctx: ctx, budget: budget, channels: make(map[uint64]*DatagramChannel), done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize), sendReady: make(chan struct{}, 1)}
	t.Cleanup(func() { cancel(); m.close() })
	return m
}

func budgetChannel(t *testing.T, m *datagramMux, id uint64) *DatagramChannel {
	t.Helper()
	c, err := m.openChannel(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func budgetFrames(t *testing.T, id uint32, payload []byte) [][]byte {
	t.Helper()
	var frames [][]byte
	if err := protocol.FragmentUDP(id, payload, func(f []byte) error { frames = append(frames, f); return nil }); err != nil {
		t.Fatal(err)
	}
	return frames
}

func budgetEnvelope(id uint64, frame []byte) []byte {
	b := make([]byte, datagramEnvelopeSize+len(frame))
	b[0], b[1], b[2] = 'R', 'U', 1
	binary.BigEndian.PutUint64(b[3:11], id)
	copy(b[datagramEnvelopeSize:], frame)
	return b
}

func budgetFragment(t *testing.T, frame []byte) protocol.UDPFragment {
	t.Helper()
	fragment, err := protocol.DecodeUDPFragment(frame)
	if err != nil {
		t.Fatal(err)
	}
	return fragment
}

type budgetStream struct{ net.Conn }

func (budgetStream) CloseWrite() error { return nil }

func budgetConn(t *testing.T, c *DatagramChannel) *UDPDatagramConn {
	t.Helper()
	a, b := net.Pipe()
	pc := NewUDPDatagramConn(c, budgetStream{Conn: a}, &net.UDPAddr{})
	t.Cleanup(func() { _ = pc.Close(); _ = b.Close() })
	return pc
}

func requireBudgetZero(t *testing.T, b *datagramBudget) {
	t.Helper()
	if usage := b.usage(); usage.Associations != 0 || usage.QueueBytes != 0 || usage.ReassemblyBytes != 0 {
		t.Fatalf("resources retained after close: %+v", usage)
	}
}

func TestDatagramIdleTimeoutCanBeDisabled(t *testing.T) {
	budget := &datagramBudget{associationLimit: 1, queueLimit: 1 << 16, reassemblyLimit: 1 << 16}
	mux := budgetMux(t, budget)
	channel := budgetChannel(t, mux, 1)
	a, b := net.Pipe()
	conn := NewUDPDatagramConnWithIdleTimeout(channel, budgetStream{Conn: a}, &net.UDPAddr{}, 0)
	defer conn.Close()
	defer b.Close()
	now := time.Now()
	conn.lastActivityNanos.Store(now.Add(-24 * time.Hour).UnixNano())
	conn.reap(now)
	select {
	case <-conn.done:
		t.Fatal("lifetime-bound datagram association was reaped as idle")
	default:
	}
}

func TestDatagramBudgetSharedAcrossMuxes(t *testing.T) {
	frame := budgetFrames(t, 1, []byte("packet"))[0]
	wireBytes := int64(datagramEnvelopeSize + len(frame))
	budget := &datagramBudget{associationLimit: 2, queueLimit: 2 * wireBytes, reassemblyLimit: 1 << 16}
	a, b := budgetMux(t, budget), budgetMux(t, budget)
	ca, cb := budgetChannel(t, a, 1), budgetChannel(t, b, 1)
	if _, err := a.openChannel(2); !errors.Is(err, ErrDatagramLimit) {
		t.Fatalf("cross-session association limit: %v", err)
	}
	if err := ca.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	b.deliver(budgetEnvelope(cb.ID, frame))
	if got := budget.usage(); got.Associations != 2 || got.QueueBytes != 2*wireBytes {
		t.Fatalf("send and receive queues did not share reservations: %+v", got)
	}
	// Overload drops both outgoing and incoming frames without closing or
	// changing the association. The accepted frames retain their reservations.
	if err := cb.Send(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	a.deliver(budgetEnvelope(ca.ID, frame))
	if got := budget.usage(); got.QueueBytes != 2*wireBytes || got.QueueDrops != 2 || got.AssociationRejects != 1 {
		t.Fatalf("overload accounting: %+v", got)
	}
	_ = ca.Close()
	if got := budget.usage(); got.Associations != 1 || got.QueueBytes != wireBytes {
		t.Fatalf("association close did not discard its send queue: %+v", got)
	}
	if err := ca.Send(context.Background(), frame); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed send: %v", err)
	}
	a.deliver(budgetEnvelope(ca.ID, frame))
	got, err := cb.Receive(context.Background())
	if err != nil || !bytes.Equal(got, frame) || budget.queueBytes.Load() != 0 {
		t.Fatalf("receive failed to transfer its reservation: frame=%x err=%v usage=%+v", got, err, budget.usage())
	}
	cc := budgetChannel(t, a, 2) // Closing one mux's channel frees the shared slot.
	_ = cc.Close()
	_ = cb.Close()
	requireBudgetZero(t, budget)
}

func TestDatagramReassemblyBudgetSharedAndReleased(t *testing.T) {
	payload := bytes.Repeat([]byte("p"), 2*protocol.UDPFragmentPayload+1)
	frames := budgetFrames(t, 9, payload)
	budget := &datagramBudget{associationLimit: 2, queueLimit: 1 << 16, reassemblyLimit: int64(len(payload))}
	a, b := budgetMux(t, budget), budgetMux(t, budget)
	pa, pb := budgetConn(t, budgetChannel(t, a, 1)), budgetConn(t, budgetChannel(t, b, 1))
	dst := make([]byte, len(payload))
	if _, complete := pa.assemble(budgetFragment(t, frames[0]), dst); complete {
		t.Fatal("incomplete packet delivered")
	}
	if got := budget.reassemblyBytes.Load(); got != int64(len(payload)) {
		t.Fatalf("expected one complete-packet reservation, got %d", got)
	}
	if _, complete := pb.assemble(budgetFragment(t, frames[0]), dst); complete || b.reassemblyBytes.Load() != 0 || len(pb.pending) != 0 {
		t.Fatal("rejected assembly retained a session reservation")
	}
	if budget.reassemblyDrops.Load() != 1 {
		t.Fatal("reassembly overload was not counted")
	}
	_, _ = pa.assemble(budgetFragment(t, frames[0]), dst) // Duplicate fragments cannot reserve again.
	_, _ = pa.assemble(budgetFragment(t, frames[2]), dst)
	n, complete := pa.assemble(budgetFragment(t, frames[1]), dst)
	if !complete || n != len(payload) || !bytes.Equal(dst, payload) || budget.reassemblyBytes.Load() != 0 {
		t.Fatalf("out-of-order completion: n=%d complete=%t usage=%+v", n, complete, budget.usage())
	}
	_, _ = pb.assemble(budgetFragment(t, frames[0]), dst)
	_ = pb.channel.Close()
	if budget.reassemblyBytes.Load() != 0 || b.reassemblyBytes.Load() != 0 {
		t.Fatalf("channel close did not synchronously release pending packet: %+v", budget.usage())
	}
	_, _ = pb.assemble(budgetFragment(t, frames[1]), dst)
	if budget.reassemblyBytes.Load() != 0 {
		t.Fatal("closed association accepted a late reassembly reservation")
	}
	_ = pa.Close()
	_ = pb.Close()
	requireBudgetZero(t, budget)
}

func TestDatagramReassemblyExpiryReleasesBudget(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), protocol.UDPFragmentPayload+1)
	budget := &datagramBudget{associationLimit: 1, queueLimit: 1 << 16, reassemblyLimit: int64(len(payload))}
	pc := budgetConn(t, budgetChannel(t, budgetMux(t, budget), 1))
	_, _ = pc.assemble(budgetFrames(t, 1, payload)[0], make([]byte, len(payload)))
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for budget.reassemblyBytes.Load() != 0 {
		select {
		case <-deadline.C:
			t.Fatal("expired assembly retained its process budget")
		case <-ticker.C:
		}
	}
	if budget.reassemblyDrops.Load() != 1 {
		t.Fatal("expired reassembly was not counted")
	}
	_ = pc.Close()
	requireBudgetZero(t, budget)
}

func TestDatagramConcurrentCloseReleasesAllQueues(t *testing.T) {
	frame := budgetFrames(t, 1, []byte("packet"))[0]
	budget := &datagramBudget{associationLimit: 8, queueLimit: 1024, reassemblyLimit: 1 << 16}
	a, b := budgetMux(t, budget), budgetMux(t, budget)
	channels := []*DatagramChannel{budgetChannel(t, a, 1), budgetChannel(t, a, 2), budgetChannel(t, b, 1), budgetChannel(t, b, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, c := range channels {
		wg.Add(3)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				_ = c.Send(ctx, frame)
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				c.mux.deliver(budgetEnvelope(c.ID, frame))
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for {
				if _, err := c.Receive(ctx); err != nil {
					return
				}
			}
		}()
	}
	close(start)
	for _, c := range channels {
		_ = c.Close()
	}
	a.close()
	b.close()
	cancel()
	wg.Wait()
	requireBudgetZero(t, budget)
}

func TestQUICDisconnectReleasesNativeBudgets(t *testing.T) {
	baseline := NativeUDPUsage()
	client, server := sessionPair(t, "quic")
	sa, sb := streamPair(t, client, server)
	ca, err := OpenDatagramChannel(client, 1)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := OpenDatagramChannel(server, 1)
	if err != nil {
		t.Fatal(err)
	}
	pa, pb := NewUDPDatagramConn(ca, sa, &net.UDPAddr{}), NewUDPDatagramConn(cb, sb, &net.UDPAddr{})
	defer pa.Close()
	defer pb.Close()
	payload := bytes.Repeat([]byte("p"), protocol.UDPFragmentPayload+1)
	frame := budgetFrames(t, 1, payload)[0]
	_, _ = pa.assemble(budgetFragment(t, frame), make([]byte, len(payload)))
	ca.mux.deliver(budgetEnvelope(ca.ID, frame))
	cb.mux.deliver(budgetEnvelope(cb.ID, frame))
	_ = client.Close()
	select {
	case <-server.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not observe the QUIC disconnect")
	}
	closed := make(chan struct{})
	go func() { server.(*QUICSession).datagrams.wg.Wait(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("native workers did not finish after disconnect")
	}
	if got := NativeUDPUsage(); got.Associations != baseline.Associations || got.QueueBytes != baseline.QueueBytes || got.ReassemblyBytes != baseline.ReassemblyBytes {
		t.Fatalf("QUIC disconnect leaked budget: before=%+v after=%+v", baseline, got)
	}
}


func TestDatagramRelayPacketReusesBackingBuffer(t *testing.T) {
	frame := budgetFrames(t, 7, []byte("relay-payload"))[0]
	budget := &datagramBudget{associationLimit: 2, queueLimit: 1 << 16, reassemblyLimit: 1 << 16}
	srcMux, dstMux := budgetMux(t, budget), budgetMux(t, budget)
	src, dst := budgetChannel(t, srcMux, 11), budgetChannel(t, dstMux, 22)

	envelope := budgetEnvelope(src.ID, frame)
	srcMux.deliver(envelope)
	packet, err := src.ReceivePacket(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if packet.PayloadBytes() != len([]byte("relay-payload")) {
		t.Fatalf("payload bytes=%d", packet.PayloadBytes())
	}
	backing := packet.packet
	if err := dst.ForwardPacket(context.Background(), packet); err != nil {
		t.Fatal(err)
	}
	if packet.packet != nil {
		t.Fatal("ForwardPacket did not consume ownership")
	}

	dstMux.mu.RLock()
	select {
	case queued := <-dstMux.send:
		dstMux.budget.releaseQueue(len(queued.packet))
		if len(queued.packet) == 0 || &queued.packet[0] != &backing[0] {
			dstMux.mu.RUnlock()
			t.Fatal("relay forwarding copied the datagram buffer")
		}
		if got := binary.BigEndian.Uint64(queued.packet[3:11]); got != dst.ID {
			dstMux.mu.RUnlock()
			t.Fatalf("forwarded association=%d want=%d", got, dst.ID)
		}
		releaseDatagramPacket(queued.packet)
	default:
		dstMux.mu.RUnlock()
		t.Fatal("forwarded datagram not queued")
	}
	dstMux.mu.RUnlock()
	_ = src.Close()
	_ = dst.Close()
	requireBudgetZero(t, budget)
}
