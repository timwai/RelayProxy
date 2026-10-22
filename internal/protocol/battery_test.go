package protocol

import (
	"encoding/json"
	"testing"
)

func TestBatteryHeartbeatBackwardCompatibility(t *testing.T) {
	var older PingMessage
	if err := json.Unmarshal([]byte(`{"timestamp":123}`), &older); err != nil {
		t.Fatal(err)
	}
	if older.Battery != nil {
		t.Fatalf("older client should not report battery: %+v", older.Battery)
	}
	wire, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"timestamp":123}` {
		t.Fatalf("battery must be omitted for older clients, got %s", wire)
	}

	updated := PingMessage{Timestamp: 456, Battery: &BatteryStatus{Level: 0, Charging: false}}
	wire, err = json.Marshal(updated)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PingMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Battery == nil || decoded.Battery.Level != 0 || decoded.Battery.Charging {
		t.Fatalf("0%% discharging battery should round trip: %+v", decoded.Battery)
	}
}

func TestBatteryLevelBounds(t *testing.T) {
	for _, tc := range []struct {
		level int
		valid bool
	}{
		{-1, false}, {0, true}, {50, true}, {100, true}, {101, false},
	} {
		if got := (BatteryStatus{Level: tc.level}).Valid(); got != tc.valid {
			t.Errorf("level %d valid=%v, want %v", tc.level, got, tc.valid)
		}
	}
}
