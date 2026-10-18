package httpproxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

// Local explicit proxy receives an IP:port authority unchanged, so a failed
// system-proxy request can be distinguished from macOS bypassing our listener.
func TestIPLiteralAuthorityReachesDialer(t *testing.T) {
	tests := []struct {
		name, request, host string
		port                uint16
		response            int
	}{
		{"http IPv4", "GET http://192.168.20.55:8090/ HTTP/1.1\r\nHost: 192.168.20.55:8090\r\n\r\n", "192.168.20.55", 8090, http.StatusBadGateway},
		{"connect IPv4", "CONNECT 203.0.113.45:8443 HTTP/1.1\r\nHost: 203.0.113.45:8443\r\n\r\n", "203.0.113.45", 8443, http.StatusGatewayTimeout},
		{"connect IPv6", "CONNECT [2001:db8::1]:8443 HTTP/1.1\r\nHost: [2001:db8::1]:8443\r\n\r\n", "2001:db8::1", 8443, http.StatusGatewayTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan string, 1)
			proxy := newLifecycleServer(t, lifecycleDialer{dial: func(_ context.Context, _ string, host string, port uint16) (net.Conn, error) {
				called <- fmt.Sprintf("%s:%d", host, port)
				return nil, errors.New("fixture: no network route")
			}})
			client := connectLifecycleClient(t, proxy)
			if _, err := client.Write([]byte(tc.request)); err != nil {
				t.Fatal(err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(client), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.response {
				t.Fatalf("status=%d, want=%d", resp.StatusCode, tc.response)
			}
			select {
			case got := <-called:
				want := fmt.Sprintf("%s:%d", tc.host, tc.port)
				if got != want {
					t.Fatalf("dialer saw %q, want %q", got, want)
				}
			default:
				t.Fatal("IP literal did not reach dialer")
			}
		})
	}
}
