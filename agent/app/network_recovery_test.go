package app

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNetworkRecoveryObserverCoalescesUnchangedSignatures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var signature atomic.Value
		signature.Store("wifi")
		var wakes atomic.Int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			watchNetworkRecovery(ctx, time.Second, func() string { return signature.Load().(string) }, func() { wakes.Add(1) })
		}()
		synctest.Wait()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if wakes.Load() != 0 {
			t.Fatal("stable network caused retries")
		}
		signature.Store("cellular")
		time.Sleep(time.Second)
		synctest.Wait()
		if wakes.Load() != 1 {
			t.Fatal("network recovery not reported")
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if wakes.Load() != 1 {
			t.Fatal("unchanged network caused repeated retries")
		}
		cancel()
		<-done
	})
}
