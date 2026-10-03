package app

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestProxyExitsFromProtocolNormalizesInventory(t *testing.T) {
	got := proxyExitsFromProtocol([]protocol.ProxyExit{
		{DeviceID: ""},
		{DeviceID: "exit-a", Name: "", IdentityName: "Team A", AuthorizationSource: "same_identity", Online: true},
		{DeviceID: "exit-a", Name: "Duplicate", Online: true},
		{DeviceID: "server", Name: "Relay Server", IdentityName: "系统资源", AuthorizationSource: "explicit", Online: true},
	})
	if len(got) != 2 {
		t.Fatalf("converted exits = %+v", got)
	}
	if got[0].DeviceID != "exit-a" || got[0].Name != "exit-a" || got[0].IdentityName != "Team A" ||
		got[0].AuthorizationSource != "same_identity" || !got[0].Online {
		t.Fatalf("unexpected first exit: %+v", got[0])
	}
	if got[1].DeviceID != "server" || got[1].Name != "Relay Server" {
		t.Fatalf("unexpected server exit: %+v", got[1])
	}
	if empty := proxyExitsFromProtocol(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty exits must remain a non-nil array: %#v", empty)
	}
}

func TestProxyExitsReturnsDefensiveCopy(t *testing.T) {
	agent := &Agent{proxyExits: []protocol.ProxyExit{{DeviceID: "exit-a", Name: "Exit A", Online: true}}}
	first := agent.ProxyExits()
	if len(first) != 1 {
		t.Fatalf("proxy exits = %+v", first)
	}
	first[0].Name = "mutated"
	second := agent.ProxyExits()
	if second[0].Name != "Exit A" {
		t.Fatalf("caller mutated agent inventory: %+v", second)
	}
}
