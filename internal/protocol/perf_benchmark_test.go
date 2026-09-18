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
