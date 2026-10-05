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


func TestRefreshProxyExitsRejectsOlderRevision(t *testing.T) {
	agent := &Agent{
		epoch:             7,
		proxyExits:        []protocol.ProxyExit{{DeviceID: "exit-new", Name: "New", Online: true}},
		proxyExitRevision: 5,
	}
	agent.refreshProxyExits(nil, 7, []protocol.ProxyExit{{DeviceID: "exit-old", Name: "Old", Online: true}}, 4)
	if got := agent.ProxyExits(); len(got) != 1 || got[0].DeviceID != "exit-new" || agent.proxyExitRevision != 5 {
		t.Fatalf("older inventory replaced current snapshot: exits=%+v revision=%d", got, agent.proxyExitRevision)
	}

	agent.refreshProxyExits(nil, 7, []protocol.ProxyExit{{DeviceID: "exit-replay", Name: "Replay", Online: true}}, 5)
	if got := agent.ProxyExits(); len(got) != 1 || got[0].DeviceID != "exit-replay" || agent.proxyExitRevision != 5 {
		t.Fatalf("same-revision recovery snapshot was ignored: exits=%+v revision=%d", got, agent.proxyExitRevision)
	}

	agent.refreshProxyExits(nil, 7, []protocol.ProxyExit{{DeviceID: "exit-latest", Name: "Latest", Online: true}}, 6)
	if got := agent.ProxyExits(); len(got) != 1 || got[0].DeviceID != "exit-latest" || agent.proxyExitRevision != 6 {
		t.Fatalf("newer inventory was not applied: exits=%+v revision=%d", got, agent.proxyExitRevision)
	}
}

func TestRefreshProxyExitsLegacyRevisionKeepsVersionCounter(t *testing.T) {
	agent := &Agent{
		epoch:             3,
		proxyExits:        []protocol.ProxyExit{{DeviceID: "exit-a", Online: true}},
		proxyExitRevision: 9,
	}
	agent.refreshProxyExits(nil, 3, []protocol.ProxyExit{{DeviceID: "exit-b", Online: true}}, 0)
	if got := agent.ProxyExits(); len(got) != 1 || got[0].DeviceID != "exit-b" {
		t.Fatalf("legacy inventory refresh was not applied: %+v", got)
	}
	if agent.proxyExitRevision != 9 {
		t.Fatalf("legacy refresh reset revision: %d", agent.proxyExitRevision)
	}
}
