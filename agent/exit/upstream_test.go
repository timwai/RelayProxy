package exit

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func startTCPEcho(t *testing.T) (net.Listener, netip.AddrPort) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().(*net.TCPAddr).AddrPort()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener, addr
}

func TestHTTPUpstreamCarriesTCPWithBasicAuth(t *testing.T) {
	targetListener, target := startTCPEcho(t)
	defer targetListener.Close()

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	auth := make(chan string, 1)
	proxyErr := make(chan error, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			proxyErr <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			proxyErr <- err
			return
		}
		auth <- req.Header.Get("Proxy-Authorization")
		upstream, err := net.Dial("tcp", req.RequestURI)
		if err != nil {
			proxyErr <- err
			return
		}
		defer upstream.Close()
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			proxyErr <- err
			return
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(upstream, reader) }()
		go func() { defer wg.Done(); _, _ = io.Copy(conn, upstream) }()
		wg.Wait()
		proxyErr <- nil
	}()

	h := NewHandler(HandlerConfig{Upstream: UpstreamConfig{
		Mode: UpstreamHTTP, Address: proxy.Addr().String(), Username: "relay", Password: "secret",
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, remote, err := h.dialTCP(ctx, []net.IP{net.ParseIP(target.Addr().String())}, target.Port())
	if err != nil {
		t.Fatal(err)
	}
	if remote != target.String() {
		t.Fatalf("remote=%q want %q", remote, target)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("through-http")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("through-http"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "through-http" {
		t.Fatalf("echo=%q", buf)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("relay:secret"))
	select {
	case got := <-auth:
		if got != wantAuth {
			t.Fatalf("Proxy-Authorization=%q want %q", got, wantAuth)
		}
	case <-ctx.Done():
		t.Fatal("HTTP proxy did not receive CONNECT")
	}
	_ = conn.Close()
	select {
	case err := <-proxyErr:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP proxy did not finish")
	}
}

func TestProxyFailureDoesNotFallBackToDirect(t *testing.T) {
	targetListener, target := startTCPEcho(t)
	defer targetListener.Close()

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddress := closed.Addr().String()
	_ = closed.Close()

	h := NewHandler(HandlerConfig{Upstream: UpstreamConfig{Mode: UpstreamHTTP, Address: proxyAddress}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, _, err := h.dialTCP(ctx, []net.IP{net.ParseIP(target.Addr().String())}, target.Port()); err == nil {
		_ = conn.Close()
		t.Fatal("unavailable proxy unexpectedly fell back to direct")
	}

	tcp := targetListener.(*net.TCPListener)
	_ = tcp.SetDeadline(time.Now().Add(150 * time.Millisecond))
	conn, err := tcp.Accept()
	if err == nil {
		_ = conn.Close()
		t.Fatal("target received a direct connection after proxy failure")
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("target accept error=%v, want timeout", err)
	}
}

func TestHTTPUpstreamRejectsUDPWithoutDirectFallback(t *testing.T) {
	h := NewHandler(HandlerConfig{Upstream: UpstreamConfig{Mode: UpstreamHTTP, Address: "127.0.0.1:8080"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := h.dialUDP(ctx, netip.MustParseAddrPort("127.0.0.1:53"))
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("HTTP upstream unexpectedly accepted UDP")
	}
}

func TestSOCKS5UpstreamCarriesTCP(t *testing.T) {
	targetListener, target := startTCPEcho(t)
	defer targetListener.Close()

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if err := acceptSOCKS5NoAuth(conn); err != nil {
			done <- err
			return
		}
		var head [4]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			done <- err
			return
		}
		if head[1] != 0x01 {
			done <- errors.New("expected SOCKS5 CONNECT")
			return
		}
		requested, err := readSOCKS5Address(conn, head[3])
		if err != nil {
			done <- err
			return
		}
		if requested != target {
			done <- errors.New("SOCKS5 CONNECT target mismatch")
			return
		}
		upstream, err := net.Dial("tcp", target.String())
		if err != nil {
			done <- err
			return
		}
		defer upstream.Close()
		reply, _ := encodeSOCKS5Address("0.0.0.0", 0)
		if _, err := conn.Write(append([]byte{0x05, 0x00, 0x00}, reply...)); err != nil {
			done <- err
			return
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = io.Copy(upstream, conn) }()
		go func() { defer wg.Done(); _, _ = io.Copy(conn, upstream) }()
		wg.Wait()
		done <- nil
	}()

	h := NewHandler(HandlerConfig{Upstream: UpstreamConfig{Mode: UpstreamSOCKS5, Address: proxy.Addr().String()}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := h.dialTCP(ctx, []net.IP{net.ParseIP(target.Addr().String())}, target.Port())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("through-socks")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("through-socks"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "through-socks" {
		t.Fatalf("echo=%q", buf)
	}
	_ = conn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS5 proxy did not finish")
	}
}

func TestSOCKS5UpstreamCarriesUDP(t *testing.T) {
	relay, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	controlReady := make(chan error, 1)
	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			controlReady <- err
			return
		}
		defer conn.Close()
		if err := acceptSOCKS5NoAuth(conn); err != nil {
			controlReady <- err
			return
		}
		var head [4]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			controlReady <- err
			return
		}
		if head[1] != 0x03 {
			controlReady <- errors.New("expected SOCKS5 UDP ASSOCIATE")
			return
		}
		if _, err := readSOCKS5Address(conn, head[3]); err != nil {
			controlReady <- err
			return
		}
		relayAddr := relay.LocalAddr().(*net.UDPAddr).AddrPort()
		reply, _ := encodeSOCKS5Address(relayAddr.Addr().String(), relayAddr.Port())
		if _, err := conn.Write(append([]byte{0x05, 0x00, 0x00}, reply...)); err != nil {
			controlReady <- err
			return
		}
		controlReady <- nil
		_, _ = io.Copy(io.Discard, conn)
	}()

	relayErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 65535)
		n, sender, err := relay.ReadFromUDP(buf)
		if err != nil {
			relayErr <- err
			return
		}
		offset, target, err := parseSOCKS5UDPAddress(buf[:n], 3)
		if err != nil {
			relayErr <- err
			return
		}
		header, err := encodeSOCKS5Address(target.Addr().String(), target.Port())
		if err != nil {
			relayErr <- err
			return
		}
		response := append([]byte{0, 0, 0}, header...)
		response = append(response, buf[offset:n]...)
		_, err = relay.WriteToUDP(response, sender)
		relayErr <- err
	}()

	target := netip.MustParseAddrPort("203.0.113.10:5353")
	h := NewHandler(HandlerConfig{Upstream: UpstreamConfig{Mode: UpstreamSOCKS5, Address: proxy.Addr().String()}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := h.dialUDP(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case err := <-controlReady:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS5 UDP control handshake timed out")
	}
	if _, err := conn.Write([]byte("udp-through-socks")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("udp-through-socks"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "udp-through-socks" {
		t.Fatalf("UDP echo=%q", buf)
	}
	select {
	case err := <-relayErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("SOCKS5 UDP relay timed out")
	}
}

func acceptSOCKS5NoAuth(conn net.Conn) error {
	var greeting [2]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return err
	}
	if greeting[0] != 0x05 || greeting[1] == 0 {
		return errors.New("invalid SOCKS5 greeting")
	}
	methods := make([]byte, int(greeting[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == 0x00 {
			_, err := conn.Write([]byte{0x05, 0x00})
			return err
		}
	}
	_, _ = conn.Write([]byte{0x05, 0xff})
	return errors.New("SOCKS5 client did not offer no-auth")
}

func TestValidateUpstreamConfig(t *testing.T) {
	for _, cfg := range []UpstreamConfig{
		{},
		{Mode: UpstreamDirect},
		{Mode: UpstreamSOCKS5, Address: "127.0.0.1:1080"},
		{Mode: UpstreamHTTP, Address: "proxy.example:8080"},
		{Mode: UpstreamHTTPS, Address: "proxy.example:443"},
	} {
		if err := ValidateUpstreamConfig(cfg); err != nil {
			t.Fatalf("valid config %+v: %v", cfg, err)
		}
	}
	for _, cfg := range []UpstreamConfig{
		{Mode: "bad"},
		{Mode: UpstreamSOCKS5, Address: "127.0.0.1"},
		{Mode: UpstreamHTTP, Address: "proxy.example:0"},
	} {
		if err := ValidateUpstreamConfig(cfg); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

