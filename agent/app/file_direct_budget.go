package app

import (
	"context"
	"time"
)

const fileDirectTicketSafetyWindow = 2 * time.Second

// fileDirectStageContext bounds one direct transport's attempts so failed
// UDP probes cannot exhaust the one-use ticket or stall Relay fallback.
func fileDirectStageContext(parent context.Context, expiresAt int64, maxDuration time.Duration) (context.Context, context.CancelFunc) {
	remaining := time.Until(time.UnixMilli(expiresAt).Add(-fileDirectTicketSafetyWindow))
	if remaining < maxDuration {
		maxDuration = remaining
	}
	if maxDuration <= 0 {
		maxDuration = time.Nanosecond
	}
	return context.WithTimeout(parent, maxDuration)
}

// Keep the authentication and FileShare request/response within the same
// stage deadline. This is cleared only after successful authentication,
// leaving large transfers controlled by their original request context.
func fileDirectHandshakeDeadline(ctx context.Context, maxDuration time.Duration) time.Time {
	deadline := time.Now().Add(maxDuration)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}
