package androidcore

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestPowerConstrainedStatusBeforeStart(t *testing.T) {
	client, err := NewClient(`{"serverAddress":"relay.example.com"}`, filepath.Join(t.TempDir(), "device-identity.json"))
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
	client, err := NewClient(`{"serverAddress":"relay.example.com"}`, filepath.Join(t.TempDir(), "device-identity.json"))
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
