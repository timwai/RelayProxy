package bridge

import (
	"relayproxy/agent/routing"
	"relayproxy/internal/exitgroup"
	"testing"
)

func TestDeviceGroupsSaveReferencesAndRename(t *testing.T) {
	b := newTestBridge(t)
	groups := []exitgroup.Group{{ID: "group:wan", Name: "主备", Members: []string{"a", "b"}}}
	selected := "group:wan"
	var in ConfigUpdate
	in.Routing = &RoutingConfigUpdate{DeviceGroups: &groups}
	in.Proxy.DefaultExitID = &selected
	if _, e := b.SaveConfig(in); e != nil {
		t.Fatal(e)
	}
	if b.agent.Config().DefaultExitID != selected {
		t.Fatal("default did not publish")
	}
	if b.agent.Config().Routing.DeviceGroups[0].Members[0] != "a" {
		t.Fatal("group order lost")
	}
	empty := []exitgroup.Group{}
	if _, e := b.SaveConfig(ConfigUpdate{Routing: &RoutingConfigUpdate{DeviceGroups: &empty}}); e == nil {
		t.Fatal("referenced group deleted")
	}
	groups[0].Name = "新名称"
	groups[0].Members = []string{"b", "a"}
	if _, e := b.SaveConfig(ConfigUpdate{Routing: &RoutingConfigUpdate{DeviceGroups: &groups}}); e != nil {
		t.Fatal(e)
	}
	state := b.GetConfig()
	if state.Proxy.DefaultExitID != selected || state.Routing.DeviceGroups[0].Name != "新名称" {
		t.Fatal("rename broke reference")
	}
	rules := []routing.Rule{{Name: "group", Enabled: true, Action: routing.ActionProxy, ExitID: selected}}
	if _, e := b.SaveConfig(ConfigUpdate{Routing: &RoutingConfigUpdate{Rules: rules}}); e != nil {
		t.Fatal(e)
	}
	if len(b.agent.Config().Routing.DeviceGroups) != 1 {
		t.Fatal("rule save erased groups")
	}
}
