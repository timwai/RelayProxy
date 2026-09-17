package routing

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"relayproxy/internal/proxy"
	"relayproxy/internal/traffic"
)

func TestManualClientMetadataDrivesPolicyAndTraffic(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	caller, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer caller.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	target, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		c, err := target.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 5)
		if _, err := io.ReadFull(c, b); err == nil {
			c.Write([]byte("reply!"))
		}
	}()
	port := uint16(target.Addr().(*net.TCPAddr).Port)
	engine, err := NewEngine(Config{Mode: ModeRule, DefaultAction: ActionReject, Rules: []Rule{{Name: "local test", Enabled: true, Processes: []string{"browser.exe"}, Targets: []string{"127.0.0.1"}, Protocols: []string{"tcp"}, Action: ActionDirect}}})
	if err != nil {
		t.Fatal(err)
	}
	dialer := NewRoutingDialer(engine, nil)
	dialer.Traffic = traffic.NewRegistry(0, 0)
	dialer.LookupProcess = func(protocol string, source, destination netip.AddrPort) (uint32, string, error) {
		if protocol != "tcp" || source.String() != caller.LocalAddr().String() || destination.String() != caller.RemoteAddr().String() {
			t.Errorf("wrong owner tuple: %s %s -> %s", protocol, source, destination)
		}
		return 1234, `C:\Apps\browser.exe`, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dialer.DialTCP(proxy.WithClientConn(ctx, "socks5", accepted), "", "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil || string(reply) != "reply!" {
		t.Fatal(string(reply), err)
	}
	conn.Close()
	s := dialer.Traffic.Snapshot()
	row := s.Connections[0]
	if s.Active != 0 || s.Upload != 5 || s.Download != 6 || row.ProcessID != 1234 || row.Entry != "socks5" || row.Rule != "local test" || row.IP != "127.0.0.1" || row.Source != caller.LocalAddr().String() {
		t.Fatalf("metadata/counters: %+v", s)
	}
	dialer.LookupProcess = func(string, netip.AddrPort, netip.AddrPort) (uint32, string, error) {
		return 0, "", errors.New("remote or unknown owner")
	}
	if _, err := dialer.DialTCP(proxy.WithClientConn(ctx, "http", accepted), "", "127.0.0.1", port); err == nil {
		t.Fatal("unknown process matched a process rule")
	}
	row = dialer.Traffic.Snapshot().Connections[0]
	if row.ProcessID != 0 || row.Process != "" || row.State != "rejected" {
		t.Fatalf("invented process: %+v", row)
	}
}
