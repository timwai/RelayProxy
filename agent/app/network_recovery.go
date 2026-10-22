package app

import (
	"context"
	"time"
)

// Desktop has no Android connectivity callback. A low-frequency interface
// observer wakes pending reconnects when usable local addresses change. It
// never closes a healthy relay because another interface appeared.
func watchNetworkRecovery(ctx context.Context, interval time.Duration, signature func() string, wake func()) {
	previous := signature()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := signature()
			if current != "" && current != previous {
				if previous != "" {
					wake()
				}
				previous = current
			}
		}
	}
}
