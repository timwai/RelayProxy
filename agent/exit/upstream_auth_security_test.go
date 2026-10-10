package exit

import (
	"io"
	"net"
	"strings"
	"testing"
)

func TestSOCKS5AuthenticationRejectsDowngrade(t *testing.T) {
	cases := []struct {
		name, user, password string
		selection            []byte
		wantError            string
	}{
		{"credentials reject no-auth", "user", "secret", []byte{5, 0}, "no-auth"},
		{"no credentials reject password auth", "", "", []byte{5, 2}, "unoffered"},
		{"invalid version", "", "", []byte{4, 0}, "rejected"},
		{"rejected methods", "", "", []byte{5, 255}, "rejected"},
		{"no-auth accepted", "", "", []byte{5, 0}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			serverDone := make(chan error, 1)
			go func() {
				var greeting [3]byte
				if _, err := io.ReadFull(server, greeting[:]); err != nil {
					serverDone <- err
					return
				}
				if greeting[0] != 5 || greeting[1] != 1 {
					serverDone <- io.ErrUnexpectedEOF
					return
				}
				_, err := server.Write(tc.selection)
				serverDone <- err
			}()
			err := socks5Authenticate(client, UpstreamConfig{Username: tc.user, Password: tc.password})
			if tc.wantError == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v; want substring %q", err, tc.wantError)
			}
			if serverErr := <-serverDone; serverErr != nil {
				t.Fatalf("server: %v", serverErr)
			}
		})
	}
}

func TestSOCKS5PasswordAuthValidatesReplyVersion(t *testing.T) {
	for _, reply := range [][]byte{{2, 0}, {1, 1}} {
		t.Run(string(rune('0'+reply[0]))+"-"+string(rune('0'+reply[1])), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				var greeting [3]byte
				if _, err := io.ReadFull(server, greeting[:]); err != nil {
					done <- err
					return
				}
				if _, err := server.Write([]byte{5, 2}); err != nil {
					done <- err
					return
				}
				auth := make([]byte, 2+len("user")+1+len("secret"))
				if _, err := io.ReadFull(server, auth); err != nil {
					done <- err
					return
				}
				_, err := server.Write(reply)
				done <- err
			}()
			if err := socks5Authenticate(client, UpstreamConfig{Username: "user", Password: "secret"}); err == nil {
				t.Fatal("accepted invalid password authentication reply")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
