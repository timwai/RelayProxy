package androidcore

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidNativeCustomExitSelectsAndSurvivesRoutingUpdate(t *testing.T) {
	config := `{
		"serverAddress":"relay.example.com",
		"identityId":"a1b2c3d4e5f6g7h8",
		"defaultExitId":"local:mobile",
		"customExits":[{
			"id":"local:mobile","name":"Local SOCKS5","enabled":true,
			"protocol":"socks5","address":"127.0.0.1:1089",
			"username":"test","password":"secret-do-not-export"
		}],
		"routing":{"mode":"rule","default_action":"PROXY",
		  "rules":[{"name":"specific","action":"PROXY","enabled":true,
		   "exit_id":"local:mobile","targets":["example.com"]}]
		}
	}`
	client, err := NewClient(config, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Stop()
	if len(client.cfg.CustomExits) != 1 || client.cfg.CustomExits[0].Password != "secret-do-not-export" {
		t.Fatal("private Android config lost its SOCKS5 password during JSON unmarshalling")
	}
	if client.proxyDialer.GetDefaultExitID() != "local:mobile" {
		t.Fatal("default exit did not reach client tunnel selector")
	}
	if client.routingDialer == nil || !client.routingDialer.CustomExitReady("local:mobile") {
		t.Fatal("Android routing dialer did not register the native custom exit")
	}
	if err := client.SetRoutingConfig(`{"mode":"global_proxy","default_action":"PROXY"}`); err != nil {
		t.Fatalf("routing hot-reload unexpectedly lost local custom exits: %v", err)
	}
	client.SetDefaultExit("local:mobile")
	var result map[string]any
	if err := json.Unmarshal([]byte(client.StatusJSON()), &result); err != nil {
		t.Fatal(err)
	}
	if result["selectedExit"] != "local:mobile" || result["proxyState"] != "ready" {
		t.Fatalf("local default was displayed as offline: %v", result)
	}
	if strings.Contains(client.StatusJSON(), "secret-do-not-export") {
		t.Fatal("local proxy password leaked through status JSON")
	}
}

func TestAndroidNativeCustomExitRejectsMissingAndDisabledReferences(t *testing.T) {
	for _, config := range []string{
		`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","defaultExitId":"local:missing"}`,
		`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8","defaultExitId":"local:mobile","customExits":[{"id":"local:mobile","name":"Off","enabled":false,"protocol":"http","address":"127.0.0.1:8899"}]}`,
	} {
		if _, err := normalizeConfig(config); err == nil {
			t.Fatalf("accepted invalid local exit reference %s", config)
		}
	}
}
