package tunnel

import (
	"context"
	"encoding/binary"
	"testing"

	"relayproxy/internal/protocol"
)

func BenchmarkDatagramForwardPacketReuse(b *testing.B) {
	budget := &datagramBudget{associationLimit: 1, queueLimit: 64 << 20, reassemblyLimit: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := &datagramMux{
		ctx: ctx, budget: budget, channels: make(map[uint64]*DatagramChannel),
		done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize), sendReady: make(chan struct{}, 1),
	}
	channel, err := mux.openChannel(9)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, protocol.UDPFragmentPayload)
	frame := make([]byte, protocol.UDPFragmentHeaderSize+len(payload))
	binary.BigEndian.PutUint32(frame[:4], 1)
	binary.BigEndian.PutUint16(frame[4:6], uint16(len(payload)))
	frame[6], frame[7] = 0, 1

	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		packet := make([]byte, datagramEnvelopeSize+len(frame))
		copy(packet[datagramEnvelopeSize:], frame)
		p := &DatagramPacket{packet: packet, payloadBytes: len(payload)}
		if err := channel.ForwardPacket(context.Background(), p); err != nil {
			b.Fatal(err)
		}
		mux.mu.RLock()
		job := <-mux.send
		mux.budget.releaseQueue(len(job.packet))
		mux.mu.RUnlock()
		releaseDatagramPacket(job.packet)
	}
	b.StopTimer()
	_ = channel.Close()
}
