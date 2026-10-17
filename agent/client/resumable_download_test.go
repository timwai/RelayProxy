package client

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	exitpkg "relayproxy/agent/exit"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func TestRelayFallbackDownloadContinuesAfterHalfClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestRead := make(chan error, 1)
	targetDone := make(chan struct{})
	go func() {
		defer close(targetDone)
		conn, err := listener.Accept()
		if err != nil {
			requestRead <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = io.ReadFull(conn, make([]byte, 2))
		requestRead <- err
		if err != nil {
			return
		}
		if _, err = io.Copy(io.Discard, conn); err != nil {
			return
		}
		_, _ = conn.Write(make([]byte, 1<<20))
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	handler := exitpkg.NewHandler(exitpkg.HandlerConfig{ResumeEnabled: true, ResumeGrace: 2 * time.Second, ResumeReplayLimit: 512 << 10})
	defer handler.Close()
	direct, relay := newHandlerSession(handler), newHandlerSession(handler)
	defer direct.Close()
	defer relay.Close()
	d := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	d.ConfigureDirectPolicy("auto", true)
	d.ConfigureDirectPath(func(string) (tunnel.TunnelSession, bool) { return direct, true }, nil)
	d.ConfigureStreamResume(true, 512<<10)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := d.DialTCP(ctx, "exit", host, uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requireTCPPath(t, conn, protocol.P2PPathDirectQUIC)
	if _, err = conn.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	direct.closeActive(t)
	r := conn.(*resumableTCPConn)
	deadline := time.Now().Add(time.Second)
	for r.currentPath() != protocol.ProxyPathRelayQUIC {
		if time.Now().After(deadline) {
			t.Fatal("Auto fallback did not reach Relay QUIC")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err = conn.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	if err = <-requestRead; err != nil {
		t.Fatal(err)
	}
	if err = r.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, readErr := io.ReadFull(conn, make([]byte, 1<<20))
	t.Logf("mode=auto current path=%s downloaded=%d of %d bytes error=%v", r.currentPath(), n, 1<<20, readErr)
	if n != 1<<20 || readErr != nil {
		t.Fatalf("download stalled after half-close: n=%d err=%v", n, readErr)
	}
	r.endpoint.Abort(net.ErrClosed)
	select {
	case <-targetDone:
	case <-time.After(time.Second):
		t.Fatal("target did not exit")
	}
}
