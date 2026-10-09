package bridge

import (
	"bytes"
	"reflect"
	"testing"

	"relayproxy/agent/routing"
	"relayproxy/internal/config"
)

func localExitUpdate(id string) CustomExitUpdate {
	return CustomExitUpdate{
		ID: id, Name: id, Protocol: "socks5", Enabled: true, Address: "127.0.0.1:10990",
	}
}

func localRule(id string) routing.Rule {
	return routing.Rule{
		Name: "custom upstream", Enabled: true, Action: routing.ActionProxy,
		Targets: []string{"example.test"}, ExitID: id,
	}
}

func TestSaveConfigPublishesNewExitWithRuleDNSAndDefault(t *testing.T) {
	b := newTestBridge(t)
	id := "local:new-proxy"
	in := ConfigUpdate{}
	in.Proxy.CustomExits = &[]CustomExitUpdate{localExitUpdate(id)}
	in.Proxy.DefaultExitID = ptr(id)
	in.Routing = &RoutingConfigUpdate{
		Mode: ptr("rule"), Rules: []routing.Rule{localRule(id)},
		DNSExitID: ptr(id),
	}
	if _, err := b.SaveConfig(in); err != nil {
		t.Fatalf("bundled new exit and routing save failed: %v", err)
	}
	runtime := b.agent.Config()
	if runtime.DefaultExitID != id || runtime.Routing.DNSExitID != id {
		t.Fatalf("inconsistent default and DNS exit: default=%q dns=%q", runtime.DefaultExitID, runtime.Routing.DNSExitID)
	}
	if len(runtime.CustomExits) != 1 || runtime.CustomExits[0].ID != id ||
		len(runtime.Routing.Rules) != 1 || runtime.Routing.Rules[0].ExitID != id {
		t.Fatalf("rules and inventory not published together: exits=%+v, rules=%+v", runtime.CustomExits, runtime.Routing.Rules)
	}
	disk, err := config.LoadAgentConfig(b.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Proxy.DefaultExitID != id || disk.Routing.DNSExitID != id ||
		!reflect.DeepEqual(disk.Proxy.CustomExits, runtime.CustomExits) {
		t.Fatalf("disk and runtime disagree after save: disk=%+v runtime=%+v", disk.Proxy, runtime.CustomExits)
	}
}

func TestSaveConfigReplacesReferencedExitInSameGeneration(t *testing.T) {
	b := newTestBridge(t)
	first := "local:first"
	initial := ConfigUpdate{}
	initial.Proxy.CustomExits = &[]CustomExitUpdate{localExitUpdate(first)}
	initial.Proxy.DefaultExitID = ptr(first)
	initial.Routing = &RoutingConfigUpdate{Rules: []routing.Rule{localRule(first)}, DNSExitID: ptr(first)}
	if _, err := b.SaveConfig(initial); err != nil {
		t.Fatal(err)
	}

	before := readConfigBytes(t, b)
	invalid := ConfigUpdate{}
	invalid.Proxy.CustomExits = &[]CustomExitUpdate{}
	if _, err := b.SaveConfig(invalid); err == nil {
		t.Fatal("deleting a referenced exit was unexpectedly accepted")
	}
	if !bytes.Equal(before, readConfigBytes(t, b)) || b.agent.Config().DefaultExitID != first {
		t.Fatal("rejected removal modified disk or live default")
	}

	second := "local:second"
	next := ConfigUpdate{}
	next.Proxy.CustomExits = &[]CustomExitUpdate{localExitUpdate(second)}
	next.Proxy.DefaultExitID = ptr(second)
	next.Routing = &RoutingConfigUpdate{Rules: []routing.Rule{localRule(second)}, DNSExitID: ptr(second)}
	if _, err := b.SaveConfig(next); err != nil {
		t.Fatalf("atomic replacement failed: %v", err)
	}
	live := b.agent.Config()
	if live.DefaultExitID != second || live.Routing.DNSExitID != second ||
		len(live.CustomExits) != 1 || live.CustomExits[0].ID != second ||
		len(live.Routing.Rules) != 1 || live.Routing.Rules[0].ExitID != second {
		t.Fatalf("replacement did not publish a single generation: %+v", live)
	}
}
