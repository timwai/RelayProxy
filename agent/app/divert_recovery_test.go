package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDivertRecoveryChecksCaptureWhileRelayStaysOnline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var running atomic.Bool
		var starts atomic.Int32
		running.Store(true)
		done := make(chan struct{})
		go func() {
			defer close(done)
			maintainDivert(ctx, func() error {
				if !running.Load() {
					starts.Add(1)
					running.Store(true)
				}
				return nil
			})
		}()
		// No Relay state callback occurs, including after a long healthy period.
		time.Sleep(time.Hour)
		synctest.Wait()
		if starts.Load() != 0 {
			t.Fatal("healthy capture was restarted")
		}
		running.Store(false)
		time.Sleep(time.Second)
		synctest.Wait()
		if !running.Load() || starts.Load() != 1 {
			t.Fatal("capture failure was not recovered independently of the Relay")
		}
		cancel()
		<-done
		running.Store(false)
		time.Sleep(time.Hour)
		if running.Load() || starts.Load() != 1 {
			t.Fatal("capture was rearmed after shutdown")
		}
	})
}

func TestDivertRecoveryBacksOffUnavailableServiceAndResetsAfterRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := time.Now()
		var attempts []time.Duration
		go maintainDivert(ctx, func() error {
			attempts = append(attempts, time.Since(started))
			if len(attempts) <= 7 {
				return errors.New("local broker unavailable")
			}
			return nil
		})
		time.Sleep(122 * time.Second)
		synctest.Wait()
		want := []time.Duration{1, 3, 7, 15, 31, 61, 91, 121, 122}
		if len(attempts) != len(want) {
			t.Fatalf("attempts=%v", attempts)
		}
		for n, seconds := range want {
			if attempts[n] != seconds*time.Second {
				t.Fatalf("attempt %d at %v, want %v seconds", n, attempts[n], seconds)
			}
		}
	})
}

func TestDivertRecoveryDoesNotOverlapSlowStartOrRetryAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release, done := make(chan struct{}), make(chan struct{})
		calls := 0
		go func() {
			defer close(done)
			maintainDivert(ctx, func() error {
				calls++
				<-release
				return errors.New("start failed")
			})
		}()
		time.Sleep(time.Minute)
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("overlapping startup attempts: %d", calls)
		}
		cancel()
		close(release)
		<-done
		time.Sleep(time.Minute)
		if calls != 1 {
			t.Fatal("cancellation scheduled another startup")
		}
	})
}
