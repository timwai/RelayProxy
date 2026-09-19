package protocol

import (
	"bytes"
	"io"
	"testing"
)

func BenchmarkReadStreamHeader(b *testing.B) {
	var wire bytes.Buffer
	if err := WriteStreamHeader(&wire, &StreamHeader{
		Magic: MagicHeader, Version: CurrentVersion, Type: FrameTypeOpenTCP,
		RequestID: "req-1234567890", ClientDeviceID: "client-1234567890", ExitDeviceID: "exit-1234567890",
	}); err != nil {
		b.Fatal(err)
	}
	frame := append([]byte(nil), wire.Bytes()...)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		reader := bytes.NewReader(frame)
		if _, err := ReadStreamHeader(reader); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadJSONSmall(b *testing.B) {
	input := OpenTCPRequest{
		RequestID: "req-123", Host: "example.com", Port: 443, TimeoutMs: 10000,
	}
	var wire bytes.Buffer
	if err := WriteJSON(&wire, input); err != nil {
		b.Fatal(err)
	}
	frame := append([]byte(nil), wire.Bytes()...)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for i := 0; i < b.N; i++ {
		reader := bytes.NewReader(frame)
		var out OpenTCPRequest
		if err := ReadJSON(reader, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteStreamHeader(b *testing.B) {
	header := &StreamHeader{
		Magic: MagicHeader, Version: CurrentVersion, Type: FrameTypeOpenTCP,
		RequestID: "req-1234567890", ClientDeviceID: "client-1234567890", ExitDeviceID: "exit-1234567890",
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := WriteStreamHeader(io.Discard, header); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteJSONSmall(b *testing.B) {
	input := OpenTCPRequest{
		RequestID: "req-123", Host: "example.com", Port: 443, TimeoutMs: 10000,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := WriteJSON(io.Discard, input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadStreamHeaderCore(b *testing.B) {
	var wire bytes.Buffer
	if err := WriteStreamHeader(&wire, &StreamHeader{
		Magic: MagicHeader, Version: CurrentVersion, Type: FrameTypeOpenTCP,
		RequestID: "req-1234567890", ClientDeviceID: "client-1234567890", ExitDeviceID: "exit-1234567890",
	}); err != nil {
		b.Fatal(err)
	}
	frame := append([]byte(nil), wire.Bytes()...)
	reader := bytes.NewReader(frame)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader.Reset(frame)
		if _, err := ReadStreamHeader(reader); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadJSONSmallCore(b *testing.B) {
	input := OpenTCPRequest{RequestID: "req-123", Host: "example.com", Port: 443, TimeoutMs: 10000}
	var wire bytes.Buffer
	if err := WriteJSON(&wire, input); err != nil {
		b.Fatal(err)
	}
	frame := append([]byte(nil), wire.Bytes()...)
	reader := bytes.NewReader(frame)
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader.Reset(frame)
		var out OpenTCPRequest
		if err := ReadJSON(reader, &out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeUDPFragment(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, UDPFragmentPayload)
	var frame []byte
	if err := FragmentUDP(7, payload, func(value []byte) error {
		frame = append(frame[:0], value...)
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeUDPFragment(frame); err != nil {
			b.Fatal(err)
		}
	}
}
