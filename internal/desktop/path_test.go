package desktop

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
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
