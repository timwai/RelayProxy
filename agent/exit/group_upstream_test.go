package exit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"relayproxy/internal/protocol"
	"sync"
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

type recordingTunnelDialer struct {
	mu     sync.Mutex
	hosts  []string
	failOn map[string]error
}

func (d *recordingTunnelDialer) DialTCP(_ context.Context, _ string, host string, _ uint16) (net.Conn, error) {
	d.mu.Lock()
	d.hosts = append(d.hosts, host)
	err := d.failOn[host]
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c, s := net.Pipe()
	_ = s.Close()
	return c, nil
}

func (d *recordingTunnelDialer) DialUDP(context.Context, string, string, uint16) (net.PacketConn, error) {
	return nil, errors.New("not used")
}

func TestGroupTCPFallsBackToNextPermittedAddress(t *testing.T) {
	d := &recordingTunnelDialer{failOn: map[string]error{"10.0.0.1": errors.New("unreachable")}}
	ips := []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2")}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, target, err := groupTCP(ctx, d, "group:home", ips, 443)
	if err != nil {
		t.Fatalf("groupTCP: %v", err)
	}
	defer conn.Close()
	if target != "10.0.0.2:443" {
		t.Fatalf("target = %q, want fallback address", target)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.hosts) < 2 {
		t.Fatalf("dialed hosts = %v, want both permitted addresses attempted", d.hosts)
	}
}

func TestGroupTCPReportsErrorWhenEveryAddressFails(t *testing.T) {
	d := &recordingTunnelDialer{failOn: map[string]error{
		"10.0.0.1": errors.New("down-1"),
		"10.0.0.2": errors.New("down-2"),
	}}
	ips := []net.IP{net.ParseIP("10.0.0.1"), net.ParseIP("10.0.0.2")}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := groupTCP(ctx, d, "group:home", ips, 443); err == nil {
		t.Fatal("expected error when all permitted addresses fail")
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
