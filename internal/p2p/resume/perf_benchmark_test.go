package resume

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func BenchmarkReadFrame(b *testing.B) {
	benchmarkReadFrames(b, false)
}

func BenchmarkFrameReader(b *testing.B) {
	benchmarkReadFrames(b, true)
}

func BenchmarkFrameWriterAck(b *testing.B) {
	var writer frameWriter
	frame := Frame{Type: FrameAck, Ack: 1}
	b.SetBytes(HeaderSize)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := writer.Write(io.Discard, frame); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFrameWriterData32KiB(b *testing.B) {
	payload := make([]byte, MaxPayload)
	frames := map[string]Frame{
		"split":    {Type: FrameData, Payload: payload},
		"combined": {Type: FrameData, Payload: payload, wire: append(make([]byte, HeaderSize), payload...)},
	}
	for name, frame := range frames {
		b.Run(name, func(b *testing.B) {
			var writer frameWriter
			b.SetBytes(MaxPayload)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := writer.Write(io.Discard, frame); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkReadFrames(b *testing.B, reuse bool) {
	for _, size := range []struct {
		name string
		n    int
	}{{"1KiB", 1 << 10}, {"32KiB", MaxPayload}} {
		b.Run(size.name, func(b *testing.B) {
			var wire bytes.Buffer
			if err := WriteFrame(&wire, Frame{Type: FrameData, Payload: make([]byte, size.n)}); err != nil {
				b.Fatal(err)
			}
			reader := bytes.NewReader(wire.Bytes())
			var decoder frameReader
			b.SetBytes(int64(size.n))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				reader.Reset(wire.Bytes())
				var err error
				if reuse {
					_, err = decoder.Read(reader)
				} else {
					_, err = ReadFrame(reader)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEndpointReceive32KiB(b *testing.B) {
	identity, err := NewIdentity()
	if err != nil {
		b.Fatal(err)
	}
	state, err := NewStreamState(identity, 1<<20)
	if err != nil {
		b.Fatal(err)
	}
	endpoint, err := NewEndpoint(state)
	if err != nil {
		b.Fatal(err)
	}
	transport, peer := net.Pipe()
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, peer)
		close(drained)
	}()
	b.Cleanup(func() {
		_ = peer.Close()
		_ = endpoint.Close()
		<-drained
	})
	if err := endpoint.Bind(transport, 1); err != nil {
		b.Fatal(err)
	}
	var wire bytes.Buffer
	if err := WriteFrame(&wire, Frame{Type: FrameData, Payload: make([]byte, MaxPayload)}); err != nil {
		b.Fatal(err)
	}
	packet := wire.Bytes()
	payload := make([]byte, MaxPayload)
	b.SetBytes(MaxPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		binary.BigEndian.PutUint64(packet[8:16], uint64(i)*MaxPayload)
		if _, err := peer.Write(packet); err != nil {
			b.Fatal(err)
		}
		if _, err := io.ReadFull(endpoint, payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEndpointData32KiB(b *testing.B) {
	state, err := NewRandomStreamState(2 * MaxPayload)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, MaxPayload)
	b.SetBytes(MaxPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		frame, err := state.dataRetained(payload)
		if err != nil {
			b.Fatal(err)
		}
		if len(frame.Payload) != MaxPayload {
			b.Fatal("unexpected payload size")
		}
		if _, _, err := state.handleBorrowed(Frame{Type: FrameAck, Ack: uint64(i+1) * MaxPayload}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStreamStateHandle32KiB(b *testing.B) {
	state, err := NewRandomStreamState(MaxPayload)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, MaxPayload)
	b.SetBytes(MaxPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		frame := Frame{Type: FrameData, Seq: uint64(i) * MaxPayload, Payload: payload}
		fresh, _, err := state.handleBorrowed(frame)
		if err != nil || len(fresh) != MaxPayload {
			b.Fatalf("fresh=%d err=%v", len(fresh), err)
		}
	}
}
