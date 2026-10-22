package androidcore

import (
	"fmt"
	"io"
	"net"
	"path/filepath"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"testing"
	"time"
)

func TestNetworkChangePreservesLocalProxyAndCoreState(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	reserved.Close()
	c, err := NewClient(fmt.Sprintf(`{"serverAddress":"127.0.0.1","tcpPort":1,"transportMode":"tcp_only","tlsEnabled":false,"identityId":"a1b2c3d4e5f6g7h8","clientEnabled":true,"socks5Enabled":true,"httpEnabled":false,"socks5Listen":%q}`, address), filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	c.NetworkChanged(false)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	handler, dialer, listener := c.handler, c.proxyDialer, c.socksServer
	for _, available := range []bool{true, false, true, false} {
		c.NetworkChanged(available)
		if c.handler != handler || c.proxyDialer != dialer || c.socksServer != listener {
			t.Fatal("network change replaced local state")
		}
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatalf("listener lost across handover: %v", err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err = conn.Write([]byte{5, 1, 0}); err != nil {
			t.Fatal(err)
		}
		var response [2]byte
		if _, err = io.ReadFull(conn, response[:]); err != nil || response != [2]byte{5, 0} {
			t.Fatalf("SOCKS handshake during handover: %v %v", response, err)
		}
		conn.Close()
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	c.NetworkChanged(true)
	if conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("Stop retained proxy listener")
	}
}

func TestDisconnectRevokesPublicDirectBeforeNextSession(t *testing.T) {
	c, err := NewClient(`{"serverAddress":"127.0.0.1","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	if c.proxyDirect == nil {
		t.Fatal("missing direct manager")
	}
	inventory := []protocol.ProxyExit{{DeviceID: "exit", Online: true, Direct: &protocol.ProxyDirectPaths{Public: &protocol.ProxyPublicDirectPath{Available: true, Transport: "quic", Ticket: []byte("old-session-ticket")}}}}
	c.updatePublicDirectInventory(inventory)
	if _, ok := c.proxyDirect.PathStatus("exit"); !ok {
		t.Fatal("test inventory was not registered")
	}
	c.onTunnelStateChange(tunnel.StateConnected, tunnel.StateDisconnected, nil)
	if _, ok := c.proxyDirect.PathStatus("exit"); ok {
		t.Fatal("disconnect retained old direct credentials")
	}
	inventory[0].Direct.Public.Ticket = []byte("new-session-ticket")
	c.updatePublicDirectInventory(inventory)
	if _, ok := c.proxyDirect.PathStatus("exit"); !ok {
		t.Fatal("replacement inventory rejected")
	}
}
