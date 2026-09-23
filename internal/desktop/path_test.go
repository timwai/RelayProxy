package desktop

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type testDatagramPath struct {
	name    string
	sendErr error
	recvErr error
	recv    []byte
	mu      sync.Mutex
	closed  bool
}

func (p *testDatagramPath) Name() string                       { return p.name }
func (p *testDatagramPath) Send(context.Context, []byte) error { return p.sendErr }
func (p *testDatagramPath) Receive(context.Context) ([]byte, error) {
	if p.recvErr != nil {
		return nil, p.recvErr
	}
	return append([]byte(nil), p.recv...), nil
}
func (p *testDatagramPath) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}
func (p *testDatagramPath) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func TestMediaConnDatagramPathName(t *testing.T) {
	conn := &MediaConn{}
	if got := conn.DatagramPathName(); got != "relay" {
		t.Fatalf("path=%q", got)
	}
	path := &testDatagramPath{name: "udp_p2p"}
	conn.SetDatagramPath(path)
	if got := conn.DatagramPathName(); got != "udp_p2p" {
		t.Fatalf("path=%q", got)
	}
	conn.ClearDatagramPath(path)
	if got := conn.DatagramPathName(); got != "relay" || !path.isClosed() {
		t.Fatalf("path=%q closed=%v", got, path.isClosed())
	}
}

func TestMediaConnReceiveClearsFailedDirectPath(t *testing.T) {
	conn := &MediaConn{}
	path := &testDatagramPath{name: "udp_p2p", recvErr: net.ErrClosed}
	conn.SetDatagramPath(path)
	_, err := conn.Receive(context.Background())
	if !errors.Is(err, net.ErrClosed) && err == nil {
		t.Fatalf("unexpected err=%v", err)
	}
	if got := conn.DatagramPathName(); got != "relay" {
		t.Fatalf("path=%q", got)
	}
	if !path.isClosed() {
		t.Fatal("failed direct path was not closed")
	}
}

type blockingDatagramPath struct {
	entered chan struct{}
	release chan struct{}

	mu         sync.Mutex
	active     int
	concurrent bool
}

func (p *blockingDatagramPath) Name() string { return "udp_p2p" }
func (p *blockingDatagramPath) Receive(context.Context) ([]byte, error) {
	return nil, net.ErrClosed
}
func (p *blockingDatagramPath) Close() error { return nil }
func (p *blockingDatagramPath) Send(ctx context.Context, _ []byte) error {
	p.mu.Lock()
	if p.active > 0 {
		p.concurrent = true
	}
	p.active++
	p.mu.Unlock()

	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}

	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return nil
}

func TestMediaConnSerializesConcurrentDatagramSends(t *testing.T) {
	path := &blockingDatagramPath{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	conn := &MediaConn{}
	conn.SetDatagramPath(path)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- conn.Send(ctx, []byte("video")) }()
	select {
	case <-path.entered:
	case <-ctx.Done():
		t.Fatal("first datagram send did not enter path")
	}
	go func() { done <- conn.Send(ctx, []byte("audio")) }()

	select {
	case <-path.entered:
		t.Fatal("second datagram send entered path concurrently")
	case <-time.After(20 * time.Millisecond):
	}

	path.release <- struct{}{}
	select {
	case <-path.entered:
	case <-ctx.Done():
		t.Fatal("second datagram send did not enter after first completed")
	}
	path.release <- struct{}{}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	path.mu.Lock()
	concurrent := path.concurrent
	path.mu.Unlock()
	if concurrent {
		t.Fatal("datagram path observed concurrent Send calls")
	}
}
