package app

import (
	"context"
	"time"
)

// maintainDivert also runs while the Relay stays online. reconcile serializes
// startup and checks authentication before acquiring system capture. Failed
// starts back off so an unavailable local service cannot cause a busy loop.
func maintainDivert(ctx context.Context, reconcile func() error) {
	delay := time.Second
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if ctx.Err() != nil {
				return
			}
			if err := reconcile(); err != nil {
				delay = min(2*delay, 30*time.Second)
			} else {
				delay = time.Second
			}
			timer.Reset(delay)
		}
	}
}
