package netutil

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestAcceptBackoffRetriesAndCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var backoff AcceptBackoff
		start := time.Now()
		for i := 0; i < 10; i++ {
			if !backoff.Wait(context.Background()) {
				t.Fatal("unexpected cancellation")
			}
		}
		if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 4*time.Second {
			t.Fatalf("retry delay not bounded: %s", elapsed)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start = time.Now()
		if backoff.Wait(ctx) || !time.Now().Equal(start) {
			t.Fatal("cancellation did not interrupt backoff")
		}
		backoff.Reset()
		start = time.Now()
		backoff.Wait(context.Background())
		if time.Since(start) > 7*time.Millisecond {
			t.Fatal("success did not reset backoff")
		}
	})
}
