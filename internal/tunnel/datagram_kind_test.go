package tunnel

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"relayproxy/internal/protocol"
)

func newFocusedDatagramMux(t *testing.T) *datagramMux {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &datagramMux{
		ctx:       ctx,
		budget:    &datagramBudget{associationLimit: 8, queueLimit: 1 << 20, reassemblyLimit: 1 << 20},
		channels:  make(map[uint64]*DatagramChannel),
		done:      make(chan struct{}),
		send:      make(chan queuedDatagram, datagramSendQueueSize),
		sendReady: make(chan struct{}, 1),
	}
}

func TestDesktopDatagramChannelRoundTrip(t *testing.T) {
	senderMux := newFocusedDatagramMux(t)
	receiverMux := newFocusedDatagramMux(t)
	sender, err := senderMux.openChannelKind(41, DatagramKindDesktopMedia)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := receiverMux.openChannelKind(41, DatagramKindDesktopMedia)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sender.Close()
		_ = receiver.Close()
	})

	payload := []byte("relay-desktop-media")
	if err := sender.Send(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	job, ok := senderMux.takeSend()
	if !ok {
		t.Fatal("desktop datagram was not queued")
	}
	if len(job.packet) < datagramEnvelopeSize || job.packet[0] != 'R' || job.packet[1] != byte(DatagramKindDesktopMedia) || job.packet[2] != 1 {
		t.Fatalf("unexpected desktop envelope: %v", job.packet[:min(len(job.packet), datagramEnvelopeSize)])
	}
	receiverMux.deliver(job.packet)
	got, err := receiver.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload = %q, want %q", got, payload)
	}
}

func TestDatagramKindIsolation(t *testing.T) {
	mux := newFocusedDatagramMux(t)
	udp, err := mux.openChannel(7)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })

	packet := make([]byte, datagramEnvelopeSize+3)
	packet[0], packet[1], packet[2] = 'R', byte(DatagramKindDesktopMedia), 1
	packet[10] = 7
	copy(packet[datagramEnvelopeSize:], []byte{1, 2, 3})
	mux.deliver(packet)
	if _, available, err := udp.takeDatagram(); err != nil || available {
		t.Fatalf("cross-kind packet available=%v err=%v", available, err)
	}
}

func TestUDPChannelStillValidatesFragments(t *testing.T) {
	mux := newFocusedDatagramMux(t)
	udp, err := mux.openChannel(9)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	if err := udp.Send(context.Background(), []byte("not-a-udp-fragment")); !errors.Is(err, protocol.ErrUDPFragment) {
		t.Fatalf("error = %v, want ErrUDPFragment", err)
	}
}

func TestDesktopForwardPacketPreservesDesktopEnvelope(t *testing.T) {
	sourceMux := newFocusedDatagramMux(t)
	relayMux := newFocusedDatagramMux(t)
	outMux := newFocusedDatagramMux(t)
	source, _ := sourceMux.openChannelKind(1, DatagramKindDesktopMedia)
	relay, _ := relayMux.openChannelKind(1, DatagramKindDesktopMedia)
	out, _ := outMux.openChannelKind(2, DatagramKindDesktopMedia)
	t.Cleanup(func() {
		_ = source.Close()
		_ = relay.Close()
		_ = out.Close()
	})

	if err := source.Send(context.Background(), []byte("encoded-frame")); err != nil {
		t.Fatal(err)
	}
	job, ok := sourceMux.takeSend()
	if !ok {
		t.Fatal("source packet missing")
	}
	relayMux.deliver(job.packet)
	packet, err := relay.ReceivePacket(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if packet.PayloadBytes() != len("encoded-frame") {
		t.Fatalf("payload bytes = %d", packet.PayloadBytes())
	}
	if err := out.ForwardPacket(context.Background(), packet); err != nil {
		t.Fatal(err)
	}
	forwarded, ok := outMux.takeSend()
	if !ok {
		t.Fatal("forwarded packet missing")
	}
	if forwarded.packet[1] != byte(DatagramKindDesktopMedia) {
		t.Fatalf("forwarded kind = %q", forwarded.packet[1])
	}
}
