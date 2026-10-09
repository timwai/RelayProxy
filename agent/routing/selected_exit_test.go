package routing

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestSelectedExitDialerUsesLocalHTTPProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	requests := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			requests <- "error: " + err.Error()
			return
		}
		requests <- req.Method + " " + req.Host
		_, _ = fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	}()

	router := &RoutingDialer{}
	router.SetCustomExits([]CustomExit{{
		ID: "local:http", Name: "HTTP", Enabled: true,
		Protocol: "http", Address: listener.Addr().String(),
	}})
	resolved := SelectedExitDialer{Routing: router}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := resolved.DialTCP(ctx, "local:http", "example.com", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case got := <-requests:
		if got != "CONNECT example.com:443" {
			t.Fatalf("unexpected HTTP proxy request: %q", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	if _, err := resolved.DialUDP(ctx, "local:http", "1.1.1.1", 53); err == nil {
		t.Fatal("HTTP custom exit must never carry UDP")
	}
	router.SetCustomExits(nil)
	if _, err := resolved.DialTCP(ctx, "local:http", "example.com", 443); err == nil {
		t.Fatal("removed exit must fail closed")
	}
}
