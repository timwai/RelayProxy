package androidcore

import (
	"testing"
	"time"
)

func TestAndroidHeartbeatPowerProfileRespectsServerTimeout(t *testing.T) {
	cases := []struct {
		heartbeatSec int
		multiplier   int32
		want         time.Duration
	}{
		{0, 1, 15 * time.Second},
		{15, 1, 15 * time.Second},
		{15, 2, 30 * time.Second},
		{30, 2, 60 * time.Second},
		{45, 2, 60 * time.Second},
		{60, 2, 60 * time.Second},
		{120, 2, 120 * time.Second}, // Do not worsen existing server configuration.
	}
	for _, tc := range cases {
		got := androidHeartbeatInterval(tc.heartbeatSec, tc.multiplier)
		if got != tc.want {
			t.Errorf("interval(%d, %d) = %v, want %v", tc.heartbeatSec, tc.multiplier, got, tc.want)
		}
		if tc.heartbeatSec > 0 && got >= time.Duration(3*tc.heartbeatSec)*time.Second+5*time.Second {
			t.Errorf("interval %v can exceed gateway read deadline", got)
		}
	}
}

func TestAndroidHeartbeatMultiplierCanChangeWithoutRestart(t *testing.T) {
	client := &Client{heartbeatWake: make(chan struct{}, 1)}
	client.SetHeartbeatMultiplier(4)
	if got := client.heartbeatMultiplier.Load(); got != 2 {
		t.Fatalf("must clamp unsafe 4x multiplier, got %d", got)
	}
	if len(client.heartbeatWake) != 1 {
		t.Fatal("expected timer wake signal")
	}
	client.SetHeartbeatMultiplier(2)
	if len(client.heartbeatWake) != 1 {
		t.Fatal("repeating unchanged profile must not enqueue another wake signal")
	}
	<-client.heartbeatWake
	client.SetHeartbeatMultiplier(1)
	if got := client.heartbeatMultiplier.Load(); got != 1 {
		t.Fatalf("expected foreground rate, got %d", got)
	}
	if len(client.heartbeatWake) != 1 {
		t.Fatal("foreground transition must wake heartbeat scheduler")
	}
	client.mu.Lock()
	client.closed = true
	client.mu.Unlock()
	client.SetHeartbeatMultiplier(2)
	if got := client.heartbeatMultiplier.Load(); got != 1 {
		t.Fatalf("closed core unexpectedly changed multiplier to %d", got)
	}
}
