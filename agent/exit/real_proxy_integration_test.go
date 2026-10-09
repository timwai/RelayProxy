package exit

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// TestLiveDanteTCPAndUDP is an opt-in integration test against an actual
// independently running SOCKS5 server (Dante in network-acceptance CI).
// It performs real TCP CONNECT and RFC1928 UDP ASSOCIATE relay traffic.
// Both targets live on loopback, so it does not depend on public DNS or
// outbound network access from CI.
func TestLiveDanteTCPAndUDP(t *testing.T) {
	proxy := os.Getenv("RELAYPROXY_LIVE_SOCKS5_ADDR")
	if proxy == "" {
		t.Skip("set RELAYPROXY_LIVE_SOCKS5_ADDR to an actual SOCKS5 proxy")
	}
	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	tcpResults := make(chan error, 1)
	go func() {
		conn, err := tcpListener.Accept()
		if err != nil {
			tcpResults <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.CopyN(conn, conn, 4)
		tcpResults <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	upstream := UpstreamConfig{Mode: UpstreamSOCKS5, Address: proxy}
	tcpTarget := tcpListener.Addr().(*net.TCPAddr)
	conn, err := DialViaUpstreamTCP(ctx, upstream, "127.0.0.1", uint16(tcpTarget.Port))
	if err != nil {
		t.Fatalf("Dante TCP CONNECT failed: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	var tcpReply [4]byte
	_, err = io.ReadFull(conn, tcpReply[:])
	_ = conn.Close()
	if err != nil || string(tcpReply[:]) != "ping" {
		t.Fatalf("Dante TCP echo response %q, error %v", tcpReply[:], err)
	}
	select {
	case err := <-tcpResults:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Dante TCP target handler timed out")
	}

	udpEcho, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	_ = udpEcho.SetDeadline(time.Now().Add(5 * time.Second))
	udpResults := make(chan error, 1)
	go func() {
		b := make([]byte, 128)
		n, from, err := udpEcho.ReadFrom(b)
		if err == nil {
			if string(b[:n]) != "udp-dante-test" {
				err = io.ErrUnexpectedEOF
			} else {
				_, err = udpEcho.WriteTo(b[:n], from)
			}
		}
		udpResults <- err
	}()
	udpTarget := udpEcho.LocalAddr().(*net.UDPAddr)
	pc, err := DialViaUpstreamUDP(ctx, upstream, "127.0.0.1", uint16(udpTarget.Port))
	if err != nil {
		t.Fatalf("Dante UDP ASSOCIATE failed: %v", err)
	}
	defer pc.Close()
	_ = pc.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := pc.WriteTo([]byte("udp-dante-test"), nil); err != nil {
		t.Fatalf("Dante UDP relay write failed: %v", err)
	}
	response := make([]byte, 128)
	n, _, err := pc.ReadFrom(response)
	if err != nil || string(response[:n]) != "udp-dante-test" {
		t.Fatalf("Dante UDP relay read failed: %q: %v", response[:n], err)
	}
	select {
	case err := <-udpResults:
		if err != nil {
			t.Fatalf("Dante UDP target failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Dante UDP target handler timed out")
	}
}

// TestLiveTinyproxyHTTPConnect verifies a separately running HTTP proxy,
// including real Basic authentication and refusal of invalid credentials.
// Tinyproxy runs only on localhost in CI; no public web endpoints are used.
func TestLiveTinyproxyHTTPConnect(t *testing.T) {
	address := os.Getenv("RELAYPROXY_LIVE_HTTP_ADDR")
	if address == "" {
		t.Skip("set RELAYPROXY_LIVE_HTTP_ADDR to an actual HTTP CONNECT proxy")
	}
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	received := make(chan error, 1)
	go func() {
		conn, err := server.Accept()
		if err != nil {
			received <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = io.CopyN(conn, conn, 5)
		received <- err
	}()
	target := uint16(server.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	upstream := UpstreamConfig{
		Mode: UpstreamHTTP, Address: address,
		Username: "relayproxy", Password: "integration-only-password",
	}
	conn, err := DialViaUpstreamTCP(ctx, upstream, "127.0.0.1", target)
	if err != nil {
		t.Fatalf("Tinyproxy authenticated CONNECT failed: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("hello")); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	var echoed [5]byte
	_, err = io.ReadFull(conn, echoed[:])
	_ = conn.Close()
	if err != nil || string(echoed[:]) != "hello" {
		t.Fatalf("Tinyproxy CONNECT echo=%q err=%v", echoed, err)
	}
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Tinyproxy target did not finish")
	}
	upstream.Password = "invalid-password"
	unauthorized, err := DialViaUpstreamTCP(ctx, upstream, "127.0.0.1", target)
	if unauthorized != nil {
		_ = unauthorized.Close()
	}
	if err == nil {
		t.Fatal("Tinyproxy accepted invalid Basic credentials")
	}
	if pc, err := DialViaUpstreamUDP(ctx, upstream, "127.0.0.1", 53); err == nil {
		_ = pc.Close()
		t.Fatal("HTTP CONNECT proxy must never silently forward UDP directly")
	}
}
