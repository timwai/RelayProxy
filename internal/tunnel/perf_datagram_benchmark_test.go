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
	payloadBytes := protocol.UDPFragmentPayload
	packetSize := datagramEnvelopeSize + protocol.UDPFragmentHeaderSize + payloadBytes
	// Warm the packet pool so the benchmark measures ownership transfer and
	// queueing, not the first backing-array allocation.
	warm := acquireDatagramPacket(packetSize)
	releaseDatagramPacket(warm)

	b.ReportAllocs()
	b.SetBytes(int64(payloadBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		packet := acquireDatagramPacket(packetSize)
		frame := packet[datagramEnvelopeSize:]
		binary.BigEndian.PutUint32(frame[:4], uint32(i+1))
		binary.BigEndian.PutUint16(frame[4:6], uint16(payloadBytes))
		frame[6], frame[7] = 0, 1
		p := DatagramPacket{packet: packet, payloadBytes: payloadBytes}
		if err := channel.ForwardPacket(context.Background(), p); err != nil {
			b.Fatal(err)
		}
		job, ok := mux.takeSend()
		if !ok {
			b.Fatal("forwarded datagram not queued")
		}
		releaseDatagramPacket(job.packet)
	}
	b.StopTimer()
	_ = channel.Close()
}
