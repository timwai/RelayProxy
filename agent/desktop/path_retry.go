package desktop

import "time"

// PathRetryPolicy controls how aggressively Relay Desktop retries an optional
// direct media path while the Relay Datagram path remains the always-available
// baseline. Short-lived P2P paths keep the backoff history so unstable NAT
// mappings cannot cause a tight reconnect loop.
type PathRetryPolicy struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	StableWindow time.Duration
}

func DefaultPathRetryPolicy() PathRetryPolicy {
	return PathRetryPolicy{
		InitialDelay: 2 * time.Second,
		MaxDelay:     30 * time.Second,
		StableWindow: 20 * time.Second,
	}
}

func (p PathRetryPolicy) Delay(failures int) time.Duration {
	initial := p.InitialDelay
	if initial <= 0 {
		initial = 2 * time.Second
	}
	maxDelay := p.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	if maxDelay < initial {
		maxDelay = initial
	}
	if failures < 0 {
		failures = 0
	}

	delay := initial
	for i := 0; i < failures && delay < maxDelay; i++ {
		if delay > maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func (p PathRetryPolicy) Stable(aliveFor time.Duration) bool {
	window := p.StableWindow
	if window <= 0 {
		window = 20 * time.Second
	}
	return aliveFor >= window
}
