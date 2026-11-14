package e2ee

import (
	"bytes"
	"io"
	"net"
	"testing"
)

func TestSecureStreamReusesRecordBuffers(t *testing.T) {
	clientConn, exitConn := net.Pipe()
	client, err := NewSecureStream(&testStream{Conn: clientConn},
		bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	exit, err := NewSecureStream(&testStream{Conn: exitConn},
		bytes.Repeat([]byte{0x42}, 32), bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = exit.Close()
	})

	payload := bytes.Repeat([]byte("buffer reuse"), 341)
	transfer := func() {
		t.Helper()
		writeDone := make(chan error, 1)
		go func() {
			n, writeErr := client.Write(payload)
			if writeErr == nil && n != len(payload) {
				writeErr = io.ErrShortWrite
			}
			writeDone <- writeErr
		}()
		got := make([]byte, len(payload))
		if _, readErr := io.ReadFull(exit, got); readErr != nil {
			t.Fatalf("read encrypted record: %v", readErr)
		}
		if writeErr := <-writeDone; writeErr != nil {
			t.Fatalf("write encrypted record: %v", writeErr)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("record payload changed")
		}
	}

	transfer()
	writeBuffer := &client.writeBuf[0]
	readBuffer := &exit.readBuf[0]
	transfer()
	if &client.writeBuf[0] != writeBuffer {
		t.Fatal("E2EE write buffer was reallocated for a same-sized record")
	}
	if &exit.readBuf[0] != readBuffer {
		t.Fatal("E2EE read buffer was reallocated for a same-sized record")
	}
}

func BenchmarkSecureStreamWrite(b *testing.B) {
	key := bytes.Repeat([]byte{0x51}, 32)
	for _, size := range []int{64, 1024, MaxRecordPlaintext, 64 * 1024} {
		b.Run(benchmarkSizeName(size), func(b *testing.B) {
			clientConn, peerConn := net.Pipe()
			stream, err := NewSecureStream(&testStream{Conn: clientConn}, key, key)
			if err != nil {
				b.Fatal(err)
			}
			peerDone := make(chan struct{})
			go func() {
				_, _ = io.Copy(io.Discard, peerConn)
				close(peerDone)
			}()
			b.Cleanup(func() {
				_ = stream.Close()
				_ = peerConn.Close()
				<-peerDone
			})
			payload := bytes.Repeat([]byte{0xA5}, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := stream.Write(payload); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSecureStreamRoundTrip(b *testing.B) {
	keyA := bytes.Repeat([]byte{0x11}, 32)
	keyB := bytes.Repeat([]byte{0x22}, 32)
	for _, size := range []int{64, 1024, MaxRecordPlaintext, 64 * 1024} {
		b.Run(benchmarkSizeName(size), func(b *testing.B) {
			clientConn, exitConn := net.Pipe()
			client, err := NewSecureStream(&testStream{Conn: clientConn}, keyA, keyB)
			if err != nil {
				b.Fatal(err)
			}
			exit, err := NewSecureStream(&testStream{Conn: exitConn}, keyB, keyA)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				_ = client.Close()
				_ = exit.Close()
			})
			payload := bytes.Repeat([]byte{0x5A}, size)
			received := make([]byte, size)
			writeDone := make(chan error, 1)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			b.ResetTimer()
			go func() {
				for i := 0; i < b.N; i++ {
					if _, writeErr := client.Write(payload); writeErr != nil {
						writeDone <- writeErr
						return
					}
				}
				writeDone <- nil
			}()
			for i := 0; i < b.N; i++ {
				if _, readErr := io.ReadFull(exit, received); readErr != nil {
					b.Fatal(readErr)
				}
			}
			if writeErr := <-writeDone; writeErr != nil {
				b.Fatal(writeErr)
			}
		})
	}
}

func benchmarkSizeName(size int) string {
	switch size {
	case 64:
		return "64B"
	case 1024:
		return "1KiB"
	case MaxRecordPlaintext:
		return "16KiB"
	default:
		return "64KiB"
	}
}
