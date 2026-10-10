//go:build windows

package gui

import (
	"encoding/json"
	"testing"

	"relayproxy/agent/bridge"
)

// Windows Wails previously returned only mode/default_action/rules,
// dropping all DNS settings on GetConfig. A full routing save then reset
// FakeIP, DoH, and TXT/SRV. Exercise the real desktop GetConfig method.
func TestWailsGetConfigPreservesDNSSettingsAcrossSave(t *testing.T) {
	for _, test := range []struct {
		name string
		json string
	}{
		{"protected", `{"routing":{"dns_mode":"proxy","auto_detect_dns":false,"fake_ip_enabled":true,"block_doh_endpoints":true,"forward_other_dns":true,"doh_blocked_ips":["1.1.1.1"]}}`},
		{"automatic", `{"routing":{"dns_mode":"proxy","auto_detect_dns":true,"fake_ip_enabled":false,"block_doh_endpoints":false,"forward_other_dns":false}}`},
		{"local", `{"routing":{"dns_mode":"local","auto_detect_dns":false,"fake_ip_enabled":false}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newWebTestBridge(t)
			var update bridge.ConfigUpdate
			if err := json.Unmarshal([]byte(test.json), &update); err != nil {
				t.Fatal(err)
			}
			if _, err := b.SaveConfig(update); err != nil {
				t.Fatal(err)
			}
			ui := &WailsService{owner: &appWindow{bridge: b}}
			raw, err := ui.GetConfig()
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Routing json.RawMessage `json:"routing"`
			}
			if err := json.Unmarshal([]byte(raw), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Routing) == 0 {
				t.Fatalf("Wails returned no routing configuration: %s", raw)
			}
			var values map[string]any
			if err := json.Unmarshal(document.Routing, &values); err != nil {
				t.Fatal(err)
			}
			var expected struct {
				Routing map[string]any `json:"routing"`
			}
			if err := json.Unmarshal([]byte(test.json), &expected); err != nil {
				t.Fatal(err)
			}
			for key, want := range expected.Routing {
				if got := values[key]; got != want {
					// arrays compare separately as JSON to avoid slice identity.
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(want)
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("Wails routing.%s = %v, want %v", key, got, want)
					}
				}
			}
			// Simulate a full-routing save from the React settings page.
			var next bridge.ConfigUpdate
			if err := json.Unmarshal([]byte(`{"routing":`+string(document.Routing)+`}`), &next); err != nil {
				t.Fatal(err)
			}
			if _, err := b.SaveConfig(next); err != nil {
				t.Fatal(err)
			}
			after, err := ui.GetConfig()
			if err != nil {
				t.Fatal(err)
			}
			var saved struct {
				Routing map[string]any `json:"routing"`
			}
			if err := json.Unmarshal([]byte(after), &saved); err != nil {
				t.Fatal(err)
			}
			for key, want := range expected.Routing {
				gotJSON, _ := json.Marshal(saved.Routing[key])
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Fatalf("Wails routing.%s lost after save: %s != %s", key, gotJSON, wantJSON)
				}
			}
		})
	}
}
