package rdp

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

type pathTestConn struct {
	net.Conn
	path string
}

func (c *pathTestConn) RDPPath() string { return c.path }

type pathTestPacketConn struct{ *controllerPacketConn }

func (*pathTestPacketConn) RDPPath() string { return "udp_p2p" }

func TestControllerReportsAcceptedTCPPathsAndClearsClosedChannels(t *testing.T) {
	for _, path := range []string{"relay", "tcp_p2p"} {
		t.Run(path, func(t *testing.T) {
			peers := make(chan net.Conn, 1)
			c, err := StartController(context.Background(), Target{DeviceID: "target"}, ControllerOptions{
				DialTCP: func(context.Context, string) (net.Conn, error) {
					conn, peer := net.Pipe()
					peers <- peer
					if path == "tcp_p2p" {
						return &pathTestConn{Conn: conn, path: path}, nil
					}
					return conn, nil
				},
			}, false)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if tcp, udp := c.PathStatus(); tcp != "waiting" || udp != "disabled" {
				t.Fatalf("idle listener advertised a data path: %s/%s", tcp, udp)
			}
			local, err := net.DialTimeout("tcp", c.Addr(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer local.Close()
			_ = local.SetDeadline(time.Now().Add(2 * time.Second))
			var peer net.Conn
			select {
			case peer = <-peers:
			case <-time.After(time.Second):
				t.Fatal("controller did not dial")
			}
			defer peer.Close()
			go func() { _, _ = peer.Write([]byte("ready")) }()
			if _, err := io.ReadFull(local, make([]byte, 5)); err != nil {
				t.Fatal(err)
			}
			if tcp, _ := c.PathStatus(); tcp != path {
				t.Fatalf("actual path=%s, want %s", tcp, path)
			}
			_ = peer.Close()
			_ = local.Close()
			deadline := time.Now().Add(time.Second)
			for {
				tcp, _ := c.PathStatus()
				if tcp == "waiting" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("closed channel retained path %s", tcp)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestControllerUDPPathFollowsAssociationReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Connection{UDPEnabled: true, cancel: cancel, done: make(chan struct{})}
	defer c.Close()
	direct := &pathTestPacketConn{newControllerPacketConn()}
	if _, err := c.remoteUDPConn(ctx, func(context.Context, string) (net.PacketConn, error) {
		return direct, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, udp := c.PathStatus(); udp != "udp_p2p" {
		t.Fatalf("direct association path=%s", udp)
	}
	c.discardRemoteUDP(direct, net.ErrClosed, true)
	if _, udp := c.PathStatus(); udp != "waiting" {
		t.Fatalf("closed association still advertised as %s", udp)
	}
	relay := newControllerPacketConn()
	if _, err := c.remoteUDPConn(ctx, func(context.Context, string) (net.PacketConn, error) {
		return relay, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, udp := c.PathStatus(); udp != "relay" {
		t.Fatalf("replacement relay still advertised as %s", udp)
	}
}
