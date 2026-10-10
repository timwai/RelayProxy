package bridge

import (
	"testing"

	"relayproxy/internal/config"
)

func TestCustomExitPasswordIsRedactedAndCanBeKeptOrCleared(t *testing.T) {
	b := newTestBridge(t)
	id := "local:authenticated"
	item := localExitUpdate(id)
	item.Username = "alice"
	item.Password = ptr("test-secret-123")
	first := ConfigUpdate{}
	first.Proxy.CustomExits = &[]CustomExitUpdate{item}
	if _, err := b.SaveConfig(first); err != nil {
		t.Fatal(err)
	}

	state, err := b.GetConfigState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Config.Proxy.CustomExits) != 1 || len(state.Runtime.Proxy.CustomExits) != 1 {
		t.Fatalf("custom exit inventory missing: %+v", state)
	}
	if e := state.Config.Proxy.CustomExits[0]; e.Password != "" || !e.HasPassword {
		t.Fatalf("stored configuration was not redacted: passwordPresent=%v hasPassword=%v", e.Password != "", e.HasPassword)
	}
	if e := state.Runtime.Proxy.CustomExits[0]; e.Password != "" || !e.HasPassword {
		t.Fatalf("running configuration was not redacted: passwordPresent=%v hasPassword=%v", e.Password != "", e.HasPassword)
	}
	readSecret := func() string {
		t.Helper()
		cfg, err := config.LoadAgentConfig(b.configPath)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Proxy.CustomExits) != 1 {
			t.Fatalf("persisted exits=%d, want 1", len(cfg.Proxy.CustomExits))
		}
		return cfg.Proxy.CustomExits[0].Password
	}
	if actual := readSecret(); actual != "test-secret-123" {
		t.Fatal("initial password was not persisted")
	}

	item.Password = nil // omitted during edit must preserve saved secret
	second := ConfigUpdate{}
	second.Proxy.CustomExits = &[]CustomExitUpdate{item}
	if _, err := b.SaveConfig(second); err != nil {
		t.Fatal(err)
	}
	if actual := readSecret(); actual != "test-secret-123" {
		t.Fatal("omitted password discarded previous secret")
	}

	item.Password = ptr("") // explicit empty string removes the password
	third := ConfigUpdate{}
	third.Proxy.CustomExits = &[]CustomExitUpdate{item}
	if _, err := b.SaveConfig(third); err != nil {
		t.Fatal(err)
	}
	if actual := readSecret(); actual != "" {
		t.Fatal("explicit clear did not remove old password")
	}
	state, err = b.GetConfigState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Config.Proxy.CustomExits[0].HasPassword || state.Runtime.Proxy.CustomExits[0].HasPassword {
		t.Fatal("cleared password is still advertised as present")
	}
}
