package androidcore

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"relayproxy/internal/protocol"
)

type messageListenerFunc func()

func (f messageListenerFunc) OnMessage() { f() }

func TestMessageListenerCoalescesAndRearmsAfterDrain(t *testing.T) {
	c := &Client{}
	var calls atomic.Int32
	c.SetMessageListener(messageListenerFunc(func() { calls.Add(1) }))
	for i := range 20 {
		c.enqueueMessage(protocol.PushMessage{ID: fmt.Sprint(i), Content: "push"})
	}
	if calls.Load() != 1 {
		t.Fatalf("one burst scheduled %d callbacks", calls.Load())
	}
	var messages []protocol.PushMessage
	if err := json.Unmarshal([]byte(c.PopMessagesJSON()), &messages); err != nil || len(messages) != 20 {
		t.Fatalf("drain lost messages: count=%d err=%v", len(messages), err)
	}
	c.enqueueMessage(protocol.PushMessage{ID: "next", Content: "push"})
	if calls.Load() != 2 {
		t.Fatal("the next burst was not signaled")
	}
}

func TestMessageListenerRegistrationDrainsPendingWithoutDeadlock(t *testing.T) {
	c := &Client{}
	c.enqueueMessage(protocol.PushMessage{ID: "pending", Content: "push"})
	var raw string
	c.SetMessageListener(messageListenerFunc(func() { raw = c.PopMessagesJSON() }))
	if raw == "" || raw == "[]" {
		t.Fatalf("registration did not signal preexisting messages: %s", raw)
	}
	c.enqueueMessage(protocol.PushMessage{ID: "new", Content: "push"})
	if c.PopMessagesJSON() != "[]" {
		t.Fatal("callback could not drain outside the client lock")
	}
}

func TestMessageListenerConcurrentDeliveryAndStop(t *testing.T) {
	c, err := NewClient(`{"serverAddress":"relay.example.com","identityId":"a1b2c3d4e5f6g7h8"}`, filepath.Join(t.TempDir(), "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Stop() })
	var delivered atomic.Int32
	c.SetMessageListener(messageListenerFunc(func() {
		var messages []protocol.PushMessage
		_ = json.Unmarshal([]byte(c.PopMessagesJSON()), &messages)
		delivered.Add(int32(len(messages)))
	}))
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.enqueueMessage(protocol.PushMessage{ID: fmt.Sprint(i), Content: "push"})
		}()
	}
	wg.Wait()
	if delivered.Load() != 100 {
		t.Fatalf("delivered %d of 100 messages", delivered.Load())
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	c.SetMessageListener(messageListenerFunc(func() { t.Error("stopped client called listener") }))
	c.enqueueMessage(protocol.PushMessage{ID: "late", Content: "push"})
	if c.messageListener != nil || c.PopMessagesJSON() != "[]" {
		t.Fatal("stopped client retained push or listener references")
	}
}
