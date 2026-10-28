package exit

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"relayproxy/internal/protocol"
	"testing"
	"time"
)

type groupTestDialer struct {
	hop int
	id  string
}

func (d *groupTestDialer) DialTCP(ctx context.Context, id, host string, port uint16) (net.Conn, error) {
	d.hop = protocol.ExitHop(ctx)
	d.id = id
	var n net.Dialer
	return n.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
}
func (d *groupTestDialer) DialUDP(ctx context.Context, id, host string, port uint16) (net.PacketConn, error) {
	d.hop = protocol.ExitHop(ctx)
	d.id = id
	return net.ListenPacket("udp4", "127.0.0.1:0")
}
func TestGroupUpstreamTCPAndUDPPreserveTargets(t *testing.T) {
	listener, target := startTCPEcho(t)
	defer listener.Close()
	d := &groupTestDialer{}
	h := NewHandler(HandlerConfig{UpstreamExitID: "group:wan", UpstreamDialer: d})
	defer h.Close()
	ctx := protocol.WithExitHop(context.Background(), 2)
	c, _, e := h.dialTCP(ctx, []net.IP{net.ParseIP("127.0.0.1")}, target.Port())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(time.Second))
	_, _ = c.Write([]byte("TCP"))
	b := make([]byte, 3)
	if _, e = io.ReadFull(c, b); e != nil || string(b) != "TCP" {
		t.Fatal(e, string(b))
	}
	if d.hop != 2 || d.id != "group:wan" {
		t.Fatal(d)
	}
	echo, addr := startGroupUDPEcho(t)
	defer echo.Close()
	u, e := h.dialUDP(ctx, addr)
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(time.Second))
	_, _ = u.Write([]byte("UDP"))
	if _, e = u.Read(b); e != nil || string(b) != "UDP" {
		t.Fatal(e, string(b))
	}
}
func TestExitRejectsForwardingLoopBeforeDial(t *testing.T) {
	for _, kind := range []protocol.FrameType{protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP} {
		var req any = protocol.OpenTCPRequest{ExitHop: protocol.MaxExitHops, Host: "127.0.0.1", Port: 9}
		if kind == protocol.FrameTypeOpenUDP {
			req = protocol.OpenUDPRequest{ExitHop: protocol.MaxExitHops, Host: "127.0.0.1", Port: 9}
		}
		resp := exitRequest(t, HandlerConfig{}, kind, req)
		if resp.Success || resp.ErrorCode != protocol.ErrCodeInvalidRequest {
			t.Fatal(resp)
		}
	}
}

func startGroupUDPEcho(t *testing.T) (net.PacketConn, netip.AddrPort) {
	t.Helper()
	p, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		b := make([]byte, 2048)
		for {
			n, a, e := p.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = p.WriteTo(b[:n], a)
		}
	}()
	return p, p.LocalAddr().(*net.UDPAddr).AddrPort()
}
