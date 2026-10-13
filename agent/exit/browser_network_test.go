package exit

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
)

type browserNetworkTestStream struct{ net.Conn }

func (s *browserNetworkTestStream) CloseWrite() error { return s.Close() }

func TestBrowserNetworkTCPFrameRequiresLocalOptIn(t *testing.T) {
	h := NewHandler(HandlerConfig{AllowBrowserNetwork: false})
	defer h.Close()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		h.HandleStreamWithHeader(context.Background(), &browserNetworkTestStream{Conn: server}, &protocol.StreamHeader{
			Type: protocol.FrameTypeBrowserNetworkTCP,
		})
		close(done)
	}()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var buf [1]byte
	if _, err := client.Read(buf[:]); err != io.EOF {
		t.Fatalf("Agent without explicit browser_network_share returned %v; expected EOF", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Agent Browser Network rejection stalled")
	}
}

func TestBrowserNetworkTCPFrameWithOptInStillEnforcesExitACL(t *testing.T) {
	denyAll, err := acl.NewChecker(acl.Policy{AllowInternet: false})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(HandlerConfig{AllowBrowserNetwork: true, ACLChecker: denyAll})
	defer h.Close()
	client, server := net.Pipe()
	defer client.Close()
	go h.HandleStreamWithHeader(context.Background(), &browserNetworkTestStream{Conn: server}, &protocol.StreamHeader{
		Type: protocol.FrameTypeBrowserNetworkTCP,
	})
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if err := protocol.WriteJSON(client, protocol.OpenTCPRequest{
		RequestID: "browser-network-test", Host: "example.com", Port: 443, TimeoutMs: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	var response protocol.OpenTCPResponse
	if err := protocol.ReadJSON(client, &response); err != nil {
		t.Fatal(err)
	}
	if response.Success || response.RequestID != "browser-network-test" || response.ErrorCode != protocol.ErrCodeACLDenied {
		t.Fatalf("Agent bypassed ordinary egress ACL: %+v", response)
	}
}
