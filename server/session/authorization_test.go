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
