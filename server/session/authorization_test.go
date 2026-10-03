package session

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRevocationAndRegistrationAreSerialized(t *testing.T) {
	m := NewManager()
	old := &DeviceSession{DeviceID: "device"}
	current := &DeviceSession{DeviceID: "device"}
	m.Register(old)
	var token atomic.Value
	token.Store("old")
	changed, release, mutationDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		_ = m.ChangeDeviceAuthorization("device", true, func() error {
			token.Store("new")
			close(changed)
			<-release
			return nil
		})
		close(mutationDone)
	}()
	<-changed
	registered := make(chan bool, 1)
	go func() { registered <- m.RegisterAuthenticated(current, func() bool { return token.Load() == "new" }) }()
	select {
	case <-registered:
		t.Fatal("new session registered before revocation completed")
	case <-time.After(30 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	<-mutationDone
	if !<-registered {
		t.Fatal("new credential was rejected")
	}
	if m.RegisterAuthenticated(old, func() bool { return token.Load() == "old" }) {
		t.Fatal("revoked credential registered")
	}
	if got, ok := m.Get("device"); !ok || got != current {
		t.Fatal("revocation or old handshake removed the new session")
	}
}

func TestInvalidateIdentityRemovesAllMatchingSessionsOnly(t *testing.T) {
	m := NewManager()
	first := &DeviceSession{DeviceID: "identity-device-a", IdentityID: "identity-a"}
	second := &DeviceSession{DeviceID: "identity-device-b", IdentityID: "identity-a"}
	other := &DeviceSession{DeviceID: "identity-device-c", IdentityID: "identity-b"}
	m.Register(first)
	m.Register(second)
	m.Register(other)

	removed := m.InvalidateIdentity("identity-a")
	if len(removed) != 2 {
		t.Fatalf("removed devices=%v want two identity-a sessions", removed)
	}
	removedSet := map[string]bool{}
	for _, deviceID := range removed {
		removedSet[deviceID] = true
	}
	if !removedSet[first.DeviceID] || !removedSet[second.DeviceID] {
		t.Fatalf("identity invalidation missed matching devices: %v", removed)
	}
	if _, ok := m.Get(first.DeviceID); ok {
		t.Fatal("first identity-a session survived invalidation")
	}
	if _, ok := m.Get(second.DeviceID); ok {
		t.Fatal("second identity-a session survived invalidation")
	}
	if got, ok := m.Get(other.DeviceID); !ok || got != other {
		t.Fatal("identity-b session was removed by identity-a invalidation")
	}
	if again := m.InvalidateIdentity("identity-a"); len(again) != 0 {
		t.Fatalf("repeated identity invalidation was not idempotent: %v", again)
	}
}
