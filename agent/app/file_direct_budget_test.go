package app

import (
	"context"
	"testing"
	"time"
)

func TestFileDirectStageContextLeavesTicketForFallback(t *testing.T) {
	now := time.Now()
	ctx, cancel := fileDirectStageContext(context.Background(), now.Add(20*time.Second).UnixMilli(), 4*time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("direct path must always have a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 3*time.Second || remaining > 4*time.Second {
		t.Fatalf("direct stage must be capped at four seconds: %s", remaining)
	}
}

func TestFileDirectStageContextRespectsTicketAndCallerDeadline(t *testing.T) {
	expiresAt := time.Now().Add(3500 * time.Millisecond).UnixMilli()
	ctx, cancel := fileDirectStageContext(context.Background(), expiresAt, 4*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if remaining := time.Until(deadline); remaining < time.Second || remaining > 1600*time.Millisecond {
		t.Fatalf("ticket safety margin not applied: %s", remaining)
	}
	parent, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer stop()
	child, childCancel := fileDirectStageContext(parent, time.Now().Add(20*time.Second).UnixMilli(), 4*time.Second)
	defer childCancel()
	childDeadline, _ := child.Deadline()
	parentDeadline, _ := parent.Deadline()
	if !childDeadline.Equal(parentDeadline) {
		t.Fatal("caller cancellation/deadline must outrank file-direct budget")
	}
	if got := fileDirectHandshakeDeadline(child, 5*time.Second); !got.Equal(parentDeadline) {
		t.Fatal("handshake deadline must not outlive stage budget")
	}
}

func TestFileDirectStageContextExpiredTicketStopsImmediately(t *testing.T) {
	ctx, cancel := fileDirectStageContext(context.Background(), time.Now().Add(-time.Second).UnixMilli(), 4*time.Second)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 100*time.Millisecond {
		t.Fatal("expired ticket must not restart a long direct path")
	}
}
