package desktop

import (
	"testing"
	"time"
)

func TestPathRetryPolicyExponentialBackoff(t *testing.T) {
	policy := DefaultPathRetryPolicy()
	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for failures, expected := range want {
		if got := policy.Delay(failures); got != expected {
			t.Fatalf("failures=%d delay=%s want=%s", failures, got, expected)
		}
	}
}

func TestPathRetryPolicyBoundsAndStableWindow(t *testing.T) {
	policy := PathRetryPolicy{
		InitialDelay: 3 * time.Second,
		MaxDelay:     5 * time.Second,
		StableWindow: 10 * time.Second,
	}
	if got := policy.Delay(-1); got != 3*time.Second {
		t.Fatalf("negative failures delay=%s", got)
	}
	if got := policy.Delay(10); got != 5*time.Second {
		t.Fatalf("capped delay=%s", got)
	}
	if policy.Stable(9 * time.Second) {
		t.Fatal("path became stable before the configured window")
	}
	if !policy.Stable(10 * time.Second) {
		t.Fatal("path did not become stable at the configured window")
	}
}
