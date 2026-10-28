//go:build linux

package gateway

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"relayproxy/agent/routing"
)

type testDialer struct {
	mu    sync.Mutex
	exits []string
}

func (d *testDialer) track(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.exits = append(d.exits, id)
}
func (d *testDialer) DialTCP(ctx context.Context, id, host string, port uint16) (net.Conn, error) {
	d.track(id)
	var n net.Dialer
	return n.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", host, port))
}
func (d *testDialer) DialUDP(ctx context.Context, id, host string, port uint16) (net.PacketConn, error) {
	d.track(id)
	return net.ListenPacket("udp4", "0.0.0.0:0")
}

// The test is opt-in because it owns nftables and policy routing inside an
// isolated privileged Linux container. Never run it on a workstation network.
func TestGatewayNetworkNamespace(t *testing.T) {
	if os.Getenv("RELAYPROXY_GATEWAY_INTEGRATION") != "1" {
		t.Skip("requires isolated privileged Linux container")
	}
	run := func(name string, args ...string) {
		t.Helper()
		out, e := exec.Command(name, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, e, out)
		}
	}
	for _, ns := range []string{"rp-client", "rp-upstream"} {
		run("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
		run("ip", "-n", ns, "link", "set", "lo", "up")
	}
	for _, p := range []struct{ host, peer, ns, ip, peerIP string }{{"gw0", "client0", "rp-client", "10.251.0.1/24", "10.251.0.2/24"}, {"uplink0", "peer0", "rp-upstream", "198.18.0.1/24", "198.18.0.2/24"}} {
		run("ip", "link", "add", p.host, "type", "veth", "peer", "name", p.peer)
		run("ip", "link", "set", p.peer, "netns", p.ns)
		run("ip", "addr", "add", p.ip, "dev", p.host)
		run("ip", "link", "set", p.host, "up")
		run("ip", "-n", p.ns, "addr", "add", p.peerIP, "dev", p.peer)
		run("ip", "-n", p.ns, "link", "set", p.peer, "up")
	}
	run("ip", "-n", "rp-client", "route", "add", "default", "via", "10.251.0.1")
	run("ip", "-n", "rp-client", "addr", "add", "10.251.1.2/24", "dev", "client0")
	run("ip", "-n", "rp-upstream", "route", "add", "default", "via", "198.18.0.1")
	run("sysctl", "-w", "net.ipv4.ip_forward=1")
	run("sysctl", "-w", "net.ipv6.conf.all.forwarding=1")
	for _, a := range []struct{ ns, iface, ip string }{{"", "gw0", "fd99::1/64"}, {"rp-client", "client0", "fd99::2/64"}, {"", "uplink0", "fdaa::1/64"}, {"rp-upstream", "peer0", "fdaa::2/64"}} {
		args := []string{"-6", "addr", "add", a.ip, "dev", a.iface, "nodad"}
		if a.ns != "" {
			args = append([]string{"-n", a.ns}, args...)
		}
		run("ip", args...)
	}
	run("ip", "-n", "rp-client", "-6", "route", "add", "default", "via", "fd99::1")
	run("ip", "-n", "rp-upstream", "-6", "route", "add", "default", "via", "fdaa::1")

	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	peer := exec.Command("ip", "netns", "exec", "rp-upstream", binary, "-test.run=^TestGatewayNetworkHelper$")
	peer.Env = append(os.Environ(), "RP_GATEWAY_HELPER=server")
	peer.Stdout = os.Stdout
	peer.Stderr = os.Stderr
	if e = peer.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = peer.Process.Kill(); _ = peer.Wait() }()
	// Poll readiness without depending on an arbitrary startup delay.
	ready := false
	for i := 0; i < 50; i++ {
		c, e := net.DialTimeout("tcp", "198.18.0.2:34001", 100*time.Millisecond)
		if e == nil {
			_ = c.Close()
			ready = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("peer did not start")
	}
	raw := &testDialer{}
	engine, e := routing.NewEngine(routing.Config{Mode: routing.ModeRule, DefaultAction: routing.ActionReject, Rules: []routing.Rule{{Name: "LAN source", Enabled: true, Entries: []string{"gateway"}, SourceCIDRs: []string{"10.251.0.0/24"}, Action: routing.ActionProxy, ExitID: "selected-by-shared-rules"}}})
	if e != nil {
		t.Fatal(e)
	}
	defer engine.Close()
	routed := routing.NewRoutingDialer(engine, raw)
	opts := Options{Config: Config{Enabled: true, Interface: "gw0", SourceCIDRs: []string{"10.251.0.0/24"}}, Dialer: routed, DNS: func(ctx context.Context, id string, q []byte, udp bool) []byte {
		var m dnsmessage.Message
		if m.Unpack(q) != nil {
			return nil
		}
		answer := dnsAnswer("echo.example", "198.18.0.2")
		var a dnsmessage.Message
		_ = a.Unpack(answer)
		a.ID = m.ID
		answer, _ = a.Pack()
		return answer
	}}
	s, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Stop(); _ = Disable() }()
	client := func(phase string) {
		t.Helper()
		c := exec.Command("ip", "netns", "exec", "rp-client", binary, "-test.run=^TestGatewayNetworkHelper$", "-test.v")
		c.Env = append(os.Environ(), "RP_GATEWAY_HELPER="+phase)
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("client %s: %v\n%s", phase, e, out)
		}
		t.Logf("client %s: %s", phase, strings.TrimSpace(string(out)))
	}
	client("forward")
	client("denied-source")
	client("ipv6-guard")
	if s.Status().TCP < 1 || s.Status().UDP < 1 || s.Status().DNS < 2 {
		t.Fatal(s.Status())
	}
	raw.mu.Lock()
	for _, id := range raw.exits {
		if id != "selected-by-shared-rules" {
			t.Fatalf("shared rule lost: %s", id)
		}
	}
	raw.mu.Unlock()
	if e = s.Stop(); e != nil {
		t.Fatal(e)
	}
	client("guard")
	// A fresh runtime can replace its retained guard and reuse its owned route.
	s, e = New(opts)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Start(); e != nil {
		t.Fatal(e)
	}
	client("forward")
	_ = s.Stop()
	if e = Disable(); e != nil {
		t.Fatal(e)
	}
	client("disabled")
}
func TestGatewayNetworkHelper(t *testing.T) {
	role := os.Getenv("RP_GATEWAY_HELPER")
	if role == "" {
		t.Skip("child helper")
	}
	if role == "ipv6-guard" {
		c, e := net.DialTimeout("tcp6", "[fdaa::2]:34001", time.Second)
		if e == nil {
			c.Close()
			t.Fatal("IPv6 bypassed gateway guard")
		}
		return
	}
	if role == "server" {
		tcp, e := net.Listen("tcp4", "198.18.0.2:34001")
		if e != nil {
			t.Fatal(e)
		}
		defer tcp.Close()
		tcp6, e := net.Listen("tcp6", "[fdaa::2]:34001")
		if e != nil {
			t.Fatal(e)
		}
		defer tcp6.Close()
		go func() {
			for {
				c, e := tcp6.Accept()
				if e != nil {
					return
				}
				go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
			}
		}()

		udp, e := net.ListenPacket("udp4", "198.18.0.2:34002")
		if e != nil {
			t.Fatal(e)
		}
		defer udp.Close()
		go func() {
			b := make([]byte, 65535)
			for {
				n, a, e := udp.ReadFrom(b)
				if e != nil {
					return
				}
				_, _ = udp.WriteTo(b[:n], a)
			}
		}()
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}
	if role == "denied-source" {
		d := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP("10.251.1.2")}}
		c, e := d.Dial("tcp4", "198.18.0.2:34001")
		if e == nil {
			c.Close()
			t.Fatal("unconfigured source forwarded")
		}
		return
	}
	conn, e := net.DialTimeout("tcp4", "198.18.0.2:34001", 2*time.Second)
	if role == "guard" {
		if e == nil {
			_ = conn.Close()
			t.Fatal("stopped gateway leaked forwarded TCP")
		}
		u, ue := net.Dial("udp4", "198.18.0.2:34002")
		if ue == nil {
			defer u.Close()
			_ = u.SetDeadline(time.Now().Add(time.Second))
			_, _ = u.Write([]byte("guard"))
			b := make([]byte, 8)
			if _, ue = u.Read(b); ue == nil {
				t.Fatal("stopped gateway leaked UDP")
			}
		}
		return
	}
	if e != nil {
		t.Fatal(e)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write([]byte("transparent TCP"))
	b := make([]byte, 15)
	if _, e = io.ReadFull(conn, b); e != nil || string(b) != "transparent TCP" {
		t.Fatalf("TCP echo %q %v", b, e)
	}
	_ = conn.Close()
	if role == "disabled" {
		c, e := net.DialTimeout("tcp6", "[fdaa::2]:34001", time.Second)
		if e != nil {
			t.Fatal("IPv6 remained blocked after explicit disable", e)
		}
		c.Close()
		return
	}
	u, e := net.Dial("udp4", "198.18.0.2:34002")
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = u.Write([]byte("UDP"))
	b = make([]byte, 32)
	n, e := u.Read(b)
	if e != nil || string(b[:n]) != "UDP" {
		t.Fatalf("UDP echo %q %v", b[:n], e)
	}
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 42, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("echo.example."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	query, _ := q.Pack()
	for _, network := range []string{"udp4", "tcp4"} {
		dns, e := net.Dial(network, "9.9.9.9:53")
		if e != nil {
			t.Fatal(e)
		}
		_ = dns.SetDeadline(time.Now().Add(3 * time.Second))
		wire := query
		if network == "tcp4" {
			wire = append([]byte{byte(len(query) >> 8), byte(len(query))}, query...)
		}
		_, _ = dns.Write(wire)
		if network == "tcp4" {
			var size [2]byte
			if _, e = io.ReadFull(dns, size[:]); e != nil {
				t.Fatal(e)
			}
			b = make([]byte, int(size[0])<<8|int(size[1]))
			_, e = io.ReadFull(dns, b)
		} else {
			b = make([]byte, 2048)
			n, e = dns.Read(b)
			b = b[:n]
		}
		_ = dns.Close()
		var a dnsmessage.Message
		if e != nil || a.Unpack(b) != nil || a.ID != 42 || len(a.Answers) != 1 {
			t.Fatalf("%s DNS reply: %x %v", network, b, e)
		}
	}
}
