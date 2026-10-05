package androidcore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	proxyp2p "relayproxy/agent/p2p"
	"relayproxy/internal/protocol"
)

func TestPowerConstrainedStatusBeforeStart(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	client.SetPowerConstrained(true)
	var status statusSnapshot
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if !status.PowerConstrained {
		t.Fatalf("powerConstrained=%v, want true", status.PowerConstrained)
	}
	if status.ConnectionState == "" {
		t.Fatal("connection state is missing")
	}

	client.SetPowerConstrained(false)
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if status.PowerConstrained {
		t.Fatal("powerConstrained remained enabled")
	}
}

func TestSetPowerConstrainedAfterStopIsIgnored(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stop(); err != nil {
		t.Fatal(err)
	}
	client.SetPowerConstrained(true)

	var status statusSnapshot
	if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
		t.Fatal(err)
	}
	if status.PowerConstrained {
		t.Fatal("closed client accepted a power-profile mutation")
	}
}

func TestIdentityIDRequiresGeneratedLetterDigitFormat(t *testing.T) {
	if !validIdentityID("a1b2c3d4e5f6g7h8") {
		t.Fatal("valid generated identity id was rejected")
	}
	for _, value := range []string{
		"team-a", "abcdefghijklmnop", "1234567890123456", "A1b2c3d4e5f6g7h8", "a1b2c3d4e5f6g7-8",
	} {
		if validIdentityID(value) {
			t.Fatalf("invalid identity id %q was accepted", value)
		}
	}
}

func TestControlOnlyConfigRequestsAndroidCapabilities(t *testing.T) {
	client, err := NewClient(`{
		"serverAddress":"relay.example.com",
		"identityId":"a1b2c3d4e5f6g7h8",
		"exitEnabled":false,
		"clientEnabled":false,
		"requestedCapabilities":["proxy.client","proxy.exit"]
	}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	want := []string{protocol.CapabilityProxyClient, protocol.CapabilityProxyExit}
	if got := client.requestedCapabilities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requested capabilities = %v, want %v", got, want)
	}
	if client.cfg.ClientEnabled || *client.cfg.ExitEnabled {
		t.Fatal("control-only config enabled a data-plane capability")
	}
}


func TestStatusJSONExposesP2PFailureReason(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "device-identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Stop() })

	manager := proxyp2p.NewManager(context.Background(), nil, nil, time.Minute)
	t.Cleanup(func() { _ = manager.Close() })
	client.mu.Lock()
	client.proxyP2P = manager
	client.status.SelectedExit = "exit-device"
	client.mu.Unlock()

	manager.EnsureClient("exit-device")
	deadline := time.Now().Add(time.Second)
	for {
		var status statusSnapshot
		if err := json.Unmarshal([]byte(client.StatusJSON()), &status); err != nil {
			t.Fatal(err)
		}
		if status.P2PError != "" {
			if status.P2PState != "COOLDOWN" {
				t.Fatalf("p2pState=%q, want COOLDOWN", status.P2PState)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("P2P failure reason was not exposed in status JSON")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
