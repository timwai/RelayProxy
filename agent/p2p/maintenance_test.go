package p2p

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestMaintenanceSleepsWithoutSessionsAndResumes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var checks atomic.Int64
		manager := NewQUICManagerWithOptions(context.Background(), nil, "", time.Minute, QUICManagerOptions{
			IdleTimeout: 4 * time.Second, LowPowerIdleTimeout: 4 * time.Second,
			NetworkCheckInterval: time.Second,
			NetworkSignature:     func() string { checks.Add(1); return "network-a" },
		})
		defer manager.Close()
		time.Sleep(time.Minute)
		if got := checks.Load(); got != 1 {
			t.Fatalf("empty manager polled interfaces %d times, want initial sample only", got)
		}
		for id := uint64(1); id <= 2; id++ {
			item := manager.newSession(id, "client", "exit", []byte("0123456789abcdef"), 0)
			item.mu.Lock()
			item.state, item.clientRole = StateReady, true
			item.mu.Unlock()
			before := checks.Load()
			time.Sleep(2 * time.Second)
			synctest.Wait()
			if checks.Load() <= before {
				t.Fatal("network watcher did not resume for a new session")
			}
			time.Sleep(3 * time.Second)
			synctest.Wait()
			if _, ok := manager.Session(id); ok {
				t.Fatal("idle reaper did not resume for a new session")
			}
			before = checks.Load()
			time.Sleep(time.Minute)
			if checks.Load() != before {
				t.Fatal("network watcher kept polling after last session expired")
			}
		}
	})
}

func TestPlatformNetworkEventsDisableInterfacePolling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var checks atomic.Int64
		manager := NewQUICManagerWithOptions(context.Background(), nil, "", time.Minute, QUICManagerOptions{
			DisableNetworkWatch: true,
			NetworkSignature:    func() string { checks.Add(1); return "network-a" },
		})
		defer manager.Close()
		manager.newSession(1, "client", "exit", []byte("0123456789abcdef"), 0)
		time.Sleep(time.Minute)
		if checks.Load() != 0 {
			t.Fatal("event-driven platform still enumerated interfaces")
		}
	})
}
