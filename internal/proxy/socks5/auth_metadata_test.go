package socks5

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/proxy"
)

type observedFlow struct {
	info     proxy.ClientInfo
	host     string
	port     uint16
	protocol string
}
type metadataDialer struct{ calls chan observedFlow }

func (d metadataDialer) DialTCP(ctx context.Context, _ string, host string, port uint16) (net.Conn, error) {
	d.calls <- observedFlow{proxy.ClientFromContext(ctx), host, port, "tcp"}
	return nil, errors.New("test destination unavailable")
}
func (d metadataDialer) DialUDP(ctx context.Context, _ string, host string, port uint16) (net.PacketConn, error) {
	d.calls <- observedFlow{proxy.ClientFromContext(ctx), host, port, "udp"}
	return nil, errors.New("test destination unavailable")
}

func authClient(t *testing.T, s *Server, username, password string, accepted bool) net.Conn {
	t.Helper()
	c := proxyClient(t, s)
	c.Write([]byte{Version5, 2, AuthMethodNone, AuthMethodUserPassword})
	var reply [2]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply != [2]byte{Version5, AuthMethodUserPassword} {
		t.Fatalf("auth negotiation: %v %v", reply, err)
	}
	msg := append([]byte{1, byte(len(username))}, []byte(username)...)
	msg = append(msg, byte(len(password)))
	msg = append(msg, []byte(password)...)
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[0] != 1 || (reply[1] == 0) != accepted {
		t.Fatalf("auth result: %v %v", reply, err)
	}
	return c
}

func TestAuthenticatedTCPAndUDPKeepSeparateApplicationOwners(t *testing.T) {
	calls := make(chan observedFlow, 8)
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: metadataDialer{calls}, Authenticate: func(user, pass string) (string, []string, bool) {
		return user, []string{user + ".shared"}, pass == "private-secret"
	}})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, s)
	for _, protocol := range []string{"tcp", "udp"} {
		for _, user := range []string{"com.example.a", "com.example.b"} {
			c := authClient(t, s, user, "private-secret", true)
			defer c.Close()
			if protocol == "tcp" {
				requestTarget(t, c, &net.TCPAddr{IP: net.IPv4(203, 0, 113, 1), Port: 443})
			} else {
				c.Write([]byte{Version5, CmdUDPAssociate, 0, AtypIPv4, 0, 0, 0, 0, 0, 0})
				reply := make([]byte, 10)
				if _, err := io.ReadFull(c, reply); err != nil || reply[1] != RepSuccess {
					t.Fatalf("UDP associate: %v %v", reply, err)
				}
				addr := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(reply[8])<<8 | int(reply[9])}
				u, err := net.DialUDP("udp", nil, addr)
				if err != nil {
					t.Fatal(err)
				}
				defer u.Close()
				packet, err := encodeUDPDatagram("203.0.113.1", 443, []byte("payload"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := u.Write(packet); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-calls:
				if got.info.Process != user || len(got.info.ProcessAliases) != 1 || got.info.ProcessAliases[0] != user+".shared" || got.protocol != protocol || got.host != "203.0.113.1" || got.port != 443 {
					t.Fatalf("misattributed flow: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("missing flow")
			}
		}
	}
	bad := authClient(t, s, "com.example.spoof", "wrong", false)
	defer bad.Close()
	assertDisconnected(t, bad)
	noAuth := proxyClient(t, s)
	defer noAuth.Close()
	noAuth.Write([]byte{Version5, 1, AuthMethodNone})
	var reply [2]byte
	if _, err := io.ReadFull(noAuth, reply[:]); err != nil || reply[1] != AuthMethodNoAcceptable {
		t.Fatal("unauthenticated VPN entry accepted", reply, err)
	}
	select {
	case got := <-calls:
		t.Fatalf("unauthenticated flow: %+v", got)
	default:
	}
}

func TestPublicSOCKSDoesNotAcceptApplicationMetadata(t *testing.T) {
	s := NewServer(ServerConfig{ListenAddr: "127.0.0.1:0", Dialer: metadataDialer{make(chan observedFlow, 1)}})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	defer closeTestServer(t, s)
	c := proxyClient(t, s)
	defer c.Close()
	c.Write([]byte{Version5, 1, AuthMethodUserPassword})
	var reply [2]byte
	if _, err := io.ReadFull(c, reply[:]); err != nil || reply[1] != AuthMethodNoAcceptable {
		t.Fatal(reply, err)
	}
}
