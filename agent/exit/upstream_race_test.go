package exit

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpstreamRacesStalledAddressAndClosesLoser(t *testing.T) {
	for _, mode := range []string{UpstreamHTTP, UpstreamSOCKS5} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			stopped, loserClosed := make(chan struct{}), make(chan struct{})
			var workers sync.WaitGroup
			go func() {
				defer close(stopped)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
						var ipv6 bool
						if mode == UpstreamHTTP {
							req, err := http.ReadRequest(bufio.NewReader(conn))
							if err != nil {
								return
							}
							ipv6 = strings.HasPrefix(req.RequestURI, "[")
							if !ipv6 {
								_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
							}
						} else {
							// no-auth greeting, followed by the literal ACL-approved target.
							greeting := make([]byte, 3)
							if _, err := io.ReadFull(conn, greeting); err != nil {
								return
							}
							if _, err := conn.Write([]byte{5, 0}); err != nil {
								return
							}
							header := make([]byte, 4)
							if _, err := io.ReadFull(conn, header); err != nil {
								return
							}
							ipv6 = header[3] == 4
							size := 6
							if ipv6 {
								size = 18
							}
							if _, err := io.ReadFull(conn, make([]byte, size)); err != nil {
								return
							}
							if !ipv6 {
								_, _ = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
							}
						}
						if ipv6 {
							_, _ = conn.Read(make([]byte, 1))
							close(loserClosed)
							return
						}
						_, _ = io.Copy(conn, conn)
					}()
				}
			}()
			defer func() { _ = listener.Close(); <-stopped; workers.Wait() }()
			handler := NewHandler(HandlerConfig{Upstream: UpstreamConfig{Mode: mode, Address: listener.Addr().String()}})
			defer handler.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, remote, err := handler.dialTCP(ctx, []net.IP{net.ParseIP("2001:db8::1"), net.ParseIP("192.0.2.1")}, 443)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if remote != "192.0.2.1:443" {
				t.Fatalf("wrong target %s", remote)
			}
			select {
			case <-loserClosed:
			case <-time.After(time.Second):
				t.Fatal("losing handshake leaked")
			}
			// The race's internal cancellation must not close the winning connection.
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			if _, err := conn.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
			payload := make([]byte, 2)
			if _, err := io.ReadFull(conn, payload); err != nil || string(payload) != "ok" {
				t.Fatalf("winner unusable: %q %v", payload, err)
			}
		})
	}
}
