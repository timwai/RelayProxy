package gui

import (
	"encoding/json"
	"reflect"
	"testing"

	"relayproxy/agent/bridge"
	"relayproxy/agent/routing"
)

// A missing key in either frontend's GetConfig response becomes a React
// default on reload. React then sends the whole routing object on Save,
// silently switching off previously enabled protections.
func TestRoutingConfigViewIncludesEveryPersistedDNSSetting(t *testing.T) {
	for _, cfg := range []routing.Config{
		{
			Mode: routing.ModeRule, DNSMode: routing.DNSModeProxy,
			FakeIPEnabled: true, BlockDoHEndpoints: true, ForwardOtherDNS: true,
			DNSExitID: "custom:resolver", DoHBlockedIPs: []string{"1.1.1.1", "9.9.9.9"},
			DefaultAction: routing.ActionProxy,
			Rules:         []routing.Rule{{Name: "Use Proxy", Action: routing.ActionProxy, Enabled: true}},
		},
		{
			Mode: routing.ModeGlobalProxy, DNSMode: routing.DNSModeProxy, AutoDetectDNS: true,
			DefaultAction: routing.ActionProxy,
		},
		{
			Mode: routing.ModeRule, DNSMode: routing.DNSModeLocal,
			DefaultAction: routing.ActionDirect,
		},
	} {
		raw, err := json.Marshal(routingConfigView(cfg))
		if err != nil {
			t.Fatal(err)
		}
		var visible map[string]json.RawMessage
		if err := json.Unmarshal(raw, &visible); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{
			"mode", "dns_mode", "auto_detect_dns", "fake_ip_enabled",
			"block_doh_endpoints", "forward_other_dns", "dns_exit_id",
			"doh_blocked_ips", "default_action", "rules",
		} {
			if _, present := visible[field]; !present {
				t.Fatalf("GetConfig omitted %q for %+v", field, cfg)
			}
		}

		var update bridge.RoutingConfigUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			t.Fatal(err)
		}
		if update.DNSMode == nil || *update.DNSMode != string(cfg.DNSMode) {
			t.Fatalf("round trip changed DNS mode: %+v", update)
		}
		if update.AutoDetectDNS == nil || *update.AutoDetectDNS != cfg.AutoDetectDNS ||
			update.FakeIPEnabled == nil || *update.FakeIPEnabled != cfg.FakeIPEnabled ||
			update.BlockDoHEndpoints == nil || *update.BlockDoHEndpoints != cfg.BlockDoHEndpoints ||
			update.ForwardOtherDNS == nil || *update.ForwardOtherDNS != cfg.ForwardOtherDNS ||
			update.DNSExitID == nil || *update.DNSExitID != cfg.DNSExitID ||
			update.DoHBlockedIPs == nil || !reflect.DeepEqual(*update.DoHBlockedIPs, defaultEmpty(cfg.DoHBlockedIPs)) {
			t.Fatalf("routing DNS fields lost on read -> save: %+v", update)
		}
	}
}

func defaultEmpty(items []string) []string {
	if items == nil {
		return []string{}
	}
	return items
}
