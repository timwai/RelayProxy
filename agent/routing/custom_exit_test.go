package routing

import (
	"strings"
	"testing"
)

func TestCustomExitReferences(t *testing.T) {
	items := []CustomExit{{
		ID: "local:test", Name: "Office SOCKS5", Enabled: true,
		Protocol: "socks5", Address: "127.0.0.1:30001",
	}}
	if err := ValidateCustomExits(items); err != nil {
		t.Fatal(err)
	}
	rules := []Rule{{Name: "work", Enabled: true, Action: ActionProxy, ExitID: "local:test"}}
	if err := ValidateCustomReferences(items, "local:test", "local:test", rules); err != nil {
		t.Fatalf("valid references refused: %v", err)
	}
	disabled := CloneCustomExits(items)
	disabled[0].Enabled = false
	if err := ValidateCustomReferences(disabled, "local:test", "", nil); err == nil {
		t.Fatal("must reject disabling the selected default exit")
	}
	if err := ValidateCustomReferences(nil, "", "local:test", nil); err == nil {
		t.Fatal("must reject deleted shared exit")
	}
	if err := ValidateCustomReferences(items, "", "remote-device", nil); err == nil {
		t.Fatal("shared upstream may not be a remote Relay device")
	}
	if err := ValidateCustomReferences(nil, "", "", []Rule{{Action: ActionProxy, ExitID: "local:missing"}}); err == nil {
		t.Fatal("must reject missing rule exit")
	}
}

func TestCustomExitValidation(t *testing.T) {
	valid := CustomExit{ID: "local:one", Name: "Proxy", Enabled: true, Protocol: "http", Address: "proxy.example:8080"}
	for _, tc := range []struct {
		name string
		edit func(*CustomExit)
	}{
		{"missing id", func(e *CustomExit) { e.ID = "remote" }},
		{"invalid port", func(e *CustomExit) { e.Address = "host:99999" }},
		{"invalid protocol", func(e *CustomExit) { e.Protocol = "direct" }},
		{"missing name", func(e *CustomExit) { e.Name = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := valid
			tc.edit(&item)
			if err := ValidateCustomExits([]CustomExit{item}); err == nil {
				t.Fatal("invalid custom exit accepted")
			}
		})
	}
	if err := ValidateCustomExits([]CustomExit{valid, valid}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate exit must fail: %v", err)
	}
}
