package androidcore

import (
	"encoding/json"
	"path/filepath"
	"testing"
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
