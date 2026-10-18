package tunnel

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

func TestYAMUXUnreadStreamCannotBufferMultipleMiB(t *testing.T) {
	a, b := net.Pipe()
	client, err := yamux.Client(a, DefaultYAMUXConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(b, DefaultYAMUXConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	stream, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receiver, err := server.AcceptStreamWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	_ = stream.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	n, err := stream.Write(bytes.Repeat([]byte{1}, 2<<20))
	if !errors.Is(err, os.ErrDeadlineExceeded) && !errors.Is(err, yamux.ErrTimeout) {
		t.Fatalf("expected flow control timeout, wrote %d: %v", n, err)
	}
	if n > 1<<20 {
		t.Fatalf("unread stream retained %d bytes, exceeding the 1 MiB window", n)
	}
}

func TestYAMUXSessionBudgetAndFullCloseRelease(t *testing.T) {
	cfg := DefaultYAMUXConfig()
	if cfg.MaxSessionBufferSize != 64<<20 {
		t.Fatalf("default aggregate receive budget = %d", cfg.MaxSessionBufferSize)
	}
	cfg.MaxSessionBufferSize = 128 << 10
	a, b := net.Pipe()
	client, err := yamux.Client(a, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := yamux.Server(b, cfg.Clone())
	if err != nil {
		t.Fatal(err)
	}
	wrapped := NewTLSSession(b, server)
	defer wrapped.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var receivers []TunnelStream
	for range 2 {
		stream, err := client.OpenStream()
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.SetDeadline(time.Now().Add(2 * time.Second))
		receiver, err := wrapped.AcceptStream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		receivers = append(receivers, receiver)
		if _, err := stream.Write(make([]byte, 64<<10)); err != nil {
			t.Fatal(err)
		}
	}
	waitYAMUXBufferBytes(t, server, 128<<10)
	overflow, err := client.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	_ = overflow.SetDeadline(time.Now().Add(time.Second))
	if _, err := overflow.Write([]byte("over budget")); err != nil {
		t.Fatal(err)
	}
	var p [1]byte
	if _, err := overflow.Read(p[:]); !errors.Is(err, yamux.ErrConnectionReset) {
		t.Fatalf("expected overflow stream reset: %v", err)
	}
	if err := receivers[0].Close(); err != nil {
		t.Fatal(err)
	}
	waitYAMUXBufferBytes(t, server, 64<<10)
	if _, err := client.Ping(); err != nil {
		t.Fatalf("session did not survive overflow: %v", err)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	waitYAMUXBufferBytes(t, server, 0)
}

func waitYAMUXBufferBytes(t *testing.T, s *yamux.Session, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for s.ReceiveBufferBytes() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := s.ReceiveBufferBytes(); got != want {
		t.Fatalf("receive capacity=%d, want %d", got, want)
	}
}
