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
		done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize),
	}
	channel, err := mux.openChannel(9)
	if err != nil {
		b.Fatal(err)
	}
	payloadBytes := protocol.UDPFragmentPayload
	packetSize := datagramEnvelopeSize + protocol.UDPFragmentHeaderSize + payloadBytes
	// Reuse one already-owned receive buffer. quic-go's receive allocation and
	// send copy are outside this benchmark; this isolates Relay's forwarding
	// envelope rewrite and queue ownership transfer.
	packet := make([]byte, packetSize)
	b.ReportAllocs()
	b.SetBytes(int64(payloadBytes))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
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
		packet = job.packet
	}
	b.StopTimer()
	_ = channel.Close()
}

func BenchmarkDatagramSendFragment1200B(b *testing.B) {
	budget := &datagramBudget{associationLimit: 1, queueLimit: 64 << 20, reassemblyLimit: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := &datagramMux{
		ctx: ctx, budget: budget, channels: make(map[uint64]*DatagramChannel),
		done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize),
	}
	channel, err := mux.openChannel(9)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, 1200)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := range b.N {
		if err := channel.sendFragment(uint32(i+1), uint16(len(payload)), 0, 1, payload); err != nil {
			b.Fatal(err)
		}
		job, ok := mux.takeSend()
		if !ok {
			b.Fatal("datagram was not queued")
		}
		releaseDatagramPacket(job.packet)
	}
	b.StopTimer()
	_ = channel.Close()
}

func BenchmarkUDPDatagramWrite1200B(b *testing.B) {
	budget := &datagramBudget{associationLimit: 1, queueLimit: 64 << 20, reassemblyLimit: 1 << 20}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := &datagramMux{
		ctx: ctx, budget: budget, channels: make(map[uint64]*DatagramChannel),
		done: make(chan struct{}), send: make(chan queuedDatagram, datagramSendQueueSize),
	}
	channel, err := mux.openChannel(9)
	if err != nil {
		b.Fatal(err)
	}
	conn := &UDPDatagramConn{channel: channel, done: make(chan struct{})}
	payload := make([]byte, 1200)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for range b.N {
		if _, err := conn.WriteTo(payload, nil); err != nil {
			b.Fatal(err)
		}
		job, ok := mux.takeSend()
		if !ok {
			b.Fatal("datagram was not queued")
		}
		releaseDatagramPacket(job.packet)
	}
	b.StopTimer()
	_ = channel.Close()
}
