package client

import (
	"context"
	"relayproxy/internal/exitgroup"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
	"sync/atomic"
	"testing"
)

func TestGroupTCPFailoverPinAndRecovery(t *testing.T) {
	var recovery atomic.Bool
	var attempts atomic.Int32
	relay := newScriptedSession(func() tunnel.TunnelStream {
		n := attempts.Add(1)
		if n == 1 && !recovery.Load() {
			return responseStream(protocol.OpenTCPResponse{ErrorCode: protocol.ErrCodeExitOffline, ErrorMessage: "offline"})
		}
		return responseStream(protocol.OpenTCPResponse{Success: true})
	})
	d := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	groups := []exitgroup.Group{{ID: "group:wan", Name: "WAN", Members: []string{"a", "b"}}}
	d.ConfigureExitGroups(groups, func(string) bool { return true })
	d.SetDefaultExitID("group:wan")
	conn, err := d.DialTCP(context.Background(), "", "192.0.2.1", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := conn.(interface{ SelectedExitID() string }).SelectedExitID(); got != "b" {
		t.Fatal(got)
	}
	requireTCPPath(t, conn, protocol.P2PPathRelayTLS)
	recovery.Store(true)
	next, err := d.DialTCP(context.Background(), "group:wan", "192.0.2.1", 443)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.(interface{ SelectedExitID() string }).SelectedExitID() != "a" {
		t.Fatal("primary not preferred after recovery")
	}
	if conn.(interface{ SelectedExitID() string }).SelectedExitID() != "b" {
		t.Fatal("existing connection migrated")
	}
	d.ConfigureExitGroups(groups, func(string) bool { return false })
	before := relay.opens.Load()
	if c, e := d.DialTCP(context.Background(), "group:wan", "192.0.2.1", 443); e == nil || c != nil {
		t.Fatal("empty group succeeded")
	}
	if relay.opens.Load() != before {
		t.Fatal("empty group dialed unconfigured exit")
	}
	if c, e := d.DialTCP(context.Background(), "group:missing", "192.0.2.1", 443); e == nil || c != nil {
		t.Fatal("missing group succeeded")
	}
}
func TestGroupUDPFailoverAndPermissionFailure(t *testing.T) {
	var attempts atomic.Int32
	relay := newScriptedSession(func() tunnel.TunnelStream {
		if attempts.Add(1) == 1 {
			return responseStream(protocol.OpenUDPResponse{ErrorCode: protocol.ErrCodeExitOffline})
		}
		return responseStream(protocol.OpenUDPResponse{Success: true})
	})
	d := NewTunnelDialer(func() tunnel.TunnelSession { return relay }, nil)
	g := []exitgroup.Group{{ID: "group:wan", Name: "WAN", Members: []string{"a", "b"}}}
	d.ConfigureExitGroups(g, nil)
	pc, e := d.DialUDP(context.Background(), "group:wan", "192.0.2.1", 53)
	if e != nil {
		t.Fatal(e)
	}
	defer pc.Close()
	if pc.(interface{ SelectedExitID() string }).SelectedExitID() != "b" {
		t.Fatal("UDP not pinned to backup")
	}
	denied := newScriptedSession(func() tunnel.TunnelStream {
		return responseStream(protocol.OpenTCPResponse{ErrorCode: protocol.ErrCodeACLDenied})
	})
	d = NewTunnelDialer(func() tunnel.TunnelSession { return denied }, nil)
	d.ConfigureExitGroups(g, nil)
	if _, e = d.DialTCP(context.Background(), "group:wan", "192.0.2.1", 443); e == nil {
		t.Fatal("ACL denial ignored")
	}
	if denied.opens.Load() != 1 {
		t.Fatal("permission denial retried")
	}
}
