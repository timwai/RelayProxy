package androidcore

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestAndroidBatteryStatusSnapshot(t *testing.T) {
	client := &Client{}
	client.SetBatteryStatus(100, true)
	got := client.batteryStatus.Load()
	if got == nil || *got != (protocol.BatteryStatus{Level: 100, Charging: true}) {
		t.Fatalf("charging snapshot not set: %+v", got)
	}
	client.SetBatteryStatus(-1, false)
	client.SetBatteryStatus(101, false)
	if next := client.batteryStatus.Load(); next != got {
		t.Fatalf("invalid level overwrote snapshot: %+v", next)
	}
	client.SetBatteryStatus(0, false)
	got = client.batteryStatus.Load()
	if got == nil || got.Level != 0 || got.Charging {
		t.Fatalf("0%% battery report lost: %+v", got)
	}
	client.mu.Lock()
	client.closed = true
	client.mu.Unlock()
	client.SetBatteryStatus(80, true)
	if next := client.batteryStatus.Load(); next != got {
		t.Fatal("closed client must not accept new reports")
	}
}
