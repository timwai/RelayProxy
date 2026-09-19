package exit

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"relayproxy/internal/acl"
	"relayproxy/internal/protocol"
)

func exitRequest(t *testing.T, cfg HandlerConfig, kind protocol.FrameType, req any) protocol.OpenTCPResponse {
	t.Helper()
	a, b := net.Pipe()
	_ = a.SetDeadline(time.Now().Add(2 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		NewHandler(cfg).HandleStream(ctx, pipeTunnelStream{Conn: b})
	}()
	defer func() {
		cancel()
		_ = a.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("exit request did not finish after cancellation")
		}
	}()
	if err := protocol.WriteStreamHeader(a, &protocol.StreamHeader{Type: kind}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteJSON(a, req); err != nil {
		t.Fatal(err)
	}
	// Both response types share these status fields.
	var resp protocol.OpenTCPResponse
	if err := protocol.ReadJSON(a, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestExitTargetACLIntersection(t *testing.T) {
	allow := acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true}
	denyLoopback := allow
	denyLoopback.AllowLoopback = false
	invalid := allow
	invalid.AccessMode = "invalid"
	for _, kind := range []protocol.FrameType{protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP} {
		name := "tcp"
		if kind == protocol.FrameTypeOpenUDP {
			name = "udp"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				local     acl.Policy
				relay     acl.Policy
				errorText string
			}{
				{"relay_blocks_resolved_address", allow, denyLoopback, "relay ACL"},
				{"local_policy_cannot_be_relaxed", denyLoopback, allow, "exit ACL"},
				{"invalid_relay_policy_is_rejected", allow, invalid, "invalid relay ACL"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					local, err := acl.NewChecker(tc.local)
					if err != nil {
						t.Fatal(err)
					}
					var req any = protocol.OpenTCPRequest{Host: "localhost", Port: 9, RelayPolicy: &tc.relay}
					if kind == protocol.FrameTypeOpenUDP {
						req = protocol.OpenUDPRequest{Host: "localhost", Port: 9, RelayPolicy: &tc.relay}
					}
					resp := exitRequest(t, HandlerConfig{ACLChecker: local}, kind, req)
					if resp.Success || resp.ErrorCode != protocol.ErrCodeACLDenied || !strings.Contains(resp.ErrorMessage, tc.errorText) {
						t.Fatalf("policy response: %+v", resp)
					}
				})
			}
		})
	}
}

func TestExitTargetACLAllowsSharedDestination(t *testing.T) {
	policy := acl.Policy{AllowInternet: true, AllowPrivateNetwork: true, AllowLoopback: true}
	local, err := acl.NewChecker(policy)
	if err != nil {
		t.Fatal(err)
	}
	resp := exitRequest(t, HandlerConfig{ACLChecker: local}, protocol.FrameTypeOpenUDP, protocol.OpenUDPRequest{Host: "127.0.0.1", Port: 9, RelayPolicy: &policy})
	if !resp.Success {
		t.Fatalf("both policies allowed destination: %+v", resp)
	}
}

func TestExitDatagramRequiredRejectsStreamTransport(t *testing.T) {
	resp := exitRequest(t, HandlerConfig{}, protocol.FrameTypeOpenUDP, protocol.OpenUDPRequest{
		Host: "127.0.0.1", Port: 9, Mode: protocol.UDPModeDatagram, AssociationID: 1, DatagramRequired: true,
	})
	if resp.Success || resp.ErrorCode != protocol.ErrCodeDatagramRequired {
		t.Fatalf("required datagram response: %+v", resp)
	}
}

func TestCompiledRelayACLCacheReusesVerifiedPolicy(t *testing.T) {
	base, err := acl.NewChecker(acl.Policy{ID: "relay", AllowInternet: true, AllowPrivateNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	policy := base.Policy()
	h := NewHandler(HandlerConfig{})
	first, err := h.compiledRelayACL(&policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.compiledRelayACL(&policy)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("compiled relay ACL cache missed identical policy")
	}

	changed := policy
	changed.AllowInternet = false
	if _, err := h.compiledRelayACL(&changed); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("stale fingerprint accepted changed policy: %v", err)
	}
}
