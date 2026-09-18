package tunnel

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func BenchmarkPipe1MiB(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, 1<<20)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		left, leftPeer := net.Pipe()
		right, rightPeer := net.Pipe()
		done := make(chan struct{})
		go func() {
			Pipe(context.Background(), left, right, 0, nil)
			close(done)
		}()
		writeDone := make(chan error, 1)
		go func() {
			_, err := leftPeer.Write(payload)
			_ = leftPeer.Close()
			writeDone <- err
		}()
		if _, err := io.Copy(io.Discard, rightPeer); err != nil {
			b.Fatal(err)
		}
		_ = rightPeer.Close()
		if err := <-writeDone; err != nil {
			b.Fatal(err)
		}
		<-done
	}
}

func BenchmarkPipe1MiBIdleTimeout(b *testing.B) {
	payload := bytes.Repeat([]byte{0x5a}, 1<<20)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		left, leftPeer := net.Pipe()
		right, rightPeer := net.Pipe()
		done := make(chan struct{})
		go func() {
			Pipe(context.Background(), left, right, 5*time.Minute, nil)
			close(done)
		}()
		writeDone := make(chan error, 1)
		go func() {
			_, err := leftPeer.Write(payload)
			_ = leftPeer.Close()
			writeDone <- err
		}()
		if _, err := io.Copy(io.Discard, rightPeer); err != nil {
			b.Fatal(err)
		}
		_ = rightPeer.Close()
		if err := <-writeDone; err != nil {
			b.Fatal(err)
		}
		<-done
	}
}

func BenchmarkYAMUXOpenStreamParallel(b *testing.B) {
	clientConn, serverConn := net.Pipe()
	clientMux, err := yamux.Client(clientConn, DefaultYAMUXConfig())
	if err != nil {
		b.Fatal(err)
	}
	serverMux, err := yamux.Server(serverConn, DefaultYAMUXConfig())
	if err != nil {
		b.Fatal(err)
	}
	client := NewTLSSession(clientConn, clientMux)
	server := NewTLSSession(serverConn, serverMux)
	ctx, cancel := context.WithCancel(context.Background())
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			stream, err := server.AcceptStream(ctx)
			if err != nil {
				return
			}
			_ = stream.Close()
		}
	}()
	b.Cleanup(func() {
		cancel()
		_ = client.Close()
		_ = server.Close()
		<-acceptDone
	})

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			stream, err := client.OpenStream(context.Background())
			if err != nil {
				b.Error(err)
				return
			}
			_ = stream.Close()
		}
	})
}
