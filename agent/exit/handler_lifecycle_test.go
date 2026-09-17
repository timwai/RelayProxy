package exit

import (
	"context"
	"net"
	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
	"testing"
	"time"
)

func TestUDPUsesProtocolACL(t *testing.T) {
	checker, err := acl.NewChecker(acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true, Rules: []acl.Rule{{Protocol: "udp", TargetType: acl.TargetAny, Action: acl.ActionDeny}}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(HandlerConfig{ACLChecker: checker})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(time.Second))
	go h.HandleStream(context.Background(), pipeTunnelStream{Conn: b})
	if err := protocol.WriteStreamHeader(a, &protocol.StreamHeader{Type: protocol.FrameTypeOpenUDP}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(a, protocol.OpenUDPRequest{Host: "127.0.0.1", Port: 53}); err != nil {
		t.Fatal(err)
	}
	var resp protocol.OpenUDPResponse
	if err := protocol.ReadJSON(a, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Success || resp.ErrorCode != protocol.ErrCodeACLDenied {
		t.Fatalf("UDP ACL response: %+v", resp)
	}
}

func TestHandlerCancellationInterruptsHandshake(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { NewHandler(HandlerConfig{}).HandleStream(ctx, pipeTunnelStream{Conn: b}); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled handler blocked in header")
	}
}
