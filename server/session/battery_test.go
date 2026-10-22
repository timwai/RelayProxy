package session

import (
	"testing"

	"relayproxy/internal/protocol"
)

func TestDeviceSessionBatterySnapshot(t *testing.T) {
	sess := &DeviceSession{}
	if got := sess.BatterySnapshot(); got != nil {
		t.Fatalf("no battery report must be unknown: %+v", got)
	}
	sess.SetBatteryStatus(nil)
	sess.SetBatteryStatus(&protocol.BatteryStatus{Level: 101, Charging: true})
	if got := sess.BatterySnapshot(); got != nil {
		t.Fatalf("invalid update must not be stored: %+v", got)
	}
	sess.SetBatteryStatus(&protocol.BatteryStatus{Level: 0, Charging: false})
	first := sess.BatterySnapshot()
	if first == nil || first.Level != 0 || first.Charging || first.ReceivedAt.IsZero() {
		t.Fatalf("valid 0%% battery not preserved: %+v", first)
	}
	sess.SetBatteryStatus(&protocol.BatteryStatus{Level: 86, Charging: true})
	second := sess.BatterySnapshot()
	if second == nil || second.Level != 86 || !second.Charging {
		t.Fatalf("new state missing: %+v", second)
	}
	sess.SetBatteryStatus(&protocol.BatteryStatus{Level: -2, Charging: false})
	if got := sess.BatterySnapshot(); got != second {
		t.Fatal("invalid report must not replace previous known battery")
	}
}
