package exit

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type observedWaitContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestDNSLeaderCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	cache := newDNSCache(time.Minute, 4)
	started, release := make(chan struct{}), make(chan struct{})
	cache.lookupIP = func(ctx context.Context, _, _ string) ([]net.IP, error) {
		close(started)
		select {
		case <-release:
			return []net.IP{net.ParseIP("192.0.2.1")}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	leader, cancel := context.WithCancel(context.Background())
	defer cancel()
	leaderResult := make(chan error, 1)
	go func() { _, err := cache.lookup(leader, "example.test"); leaderResult <- err }()
	<-started
	followerCtx, cancelFollower := context.WithTimeout(context.Background(), time.Second)
	defer cancelFollower()
	follower := &observedWaitContext{Context: followerCtx, entered: make(chan struct{})}
	followerResult := make(chan error, 1)
	go func() { _, err := cache.lookup(follower, "example.test"); followerResult <- err }()
	<-follower.entered
	cancel()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader=%v", err)
	}
	close(release)
	if err := <-followerResult; err != nil {
		t.Fatalf("healthy waiter poisoned: %v", err)
	}
}

func TestDNSCancelsAbandonedLookupAndAllowsRetry(t *testing.T) {
	cache := newDNSCache(time.Minute, 4)
	started, stopped := make(chan struct{}), make(chan struct{})
	cache.lookupIP = func(ctx context.Context, _, _ string) ([]net.IP, error) {
		close(started)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("shared lookup has no lifetime bound")
		}
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := cache.lookup(ctx, "example.test"); result <- err }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("abandoned resolver leaked")
	}
	// Cancellation detaches the flight before returning to its last waiter.
	cache.mu.RLock()
	flight := cache.inflight["example.test"]
	cache.mu.RUnlock()
	if flight != nil {
		t.Fatal("abandoned lookup still accepts new waiters")
	}
}

func TestDNSNewWaiterDoesNotJoinAbandonedFlight(t *testing.T) {
	cache := newDNSCache(time.Minute, 4)
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	cache.lookupIP = func(ctx context.Context, _, _ string) ([]net.IP, error) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(started)
			<-release
			return nil, context.Canceled
		}
		return []net.IP{net.ParseIP("192.0.2.2")}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := cache.lookup(ctx, "example.test"); result <- err }()
	<-started
	cancel()
	<-result
	defer close(release)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), time.Second)
	defer retryCancel()
	ips, err := cache.lookup(retryCtx, "example.test")
	if err != nil || len(ips) != 1 || ips[0].String() != "192.0.2.2" {
		t.Fatalf("retry joined abandoned flight: %v %v", ips, err)
	}
}
