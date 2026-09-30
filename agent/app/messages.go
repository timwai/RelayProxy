package app

import (
	"sync"

	"relayproxy/internal/protocol"
)

const defaultMessageHistorySize = 500

// Message is the Agent/UI view of a Relay push notification.
type Message = protocol.PushMessage

type MessageBuffer struct {
	mu      sync.RWMutex
	entries []Message
	maxSize int
	tap     func(Message)
}

func NewMessageBuffer(maxSize int) *MessageBuffer {
	if maxSize <= 0 {
		maxSize = defaultMessageHistorySize
	}
	return &MessageBuffer{maxSize: maxSize}
}

func (b *MessageBuffer) Add(message Message) {
	if b == nil || message.ID == "" {
		return
	}
	b.mu.Lock()
	for i := range b.entries {
		if b.entries[i].ID == message.ID {
			b.entries[i] = message
			tap := b.tap
			b.mu.Unlock()
			if tap != nil {
				tap(message)
			}
			return
		}
	}
	if len(b.entries) >= b.maxSize {
		copy(b.entries, b.entries[len(b.entries)-b.maxSize+1:])
		b.entries = b.entries[:b.maxSize-1]
	}
	b.entries = append(b.entries, message)
	tap := b.tap
	b.mu.Unlock()
	if tap != nil {
		tap(message)
	}
}

func (b *MessageBuffer) Restore(messages []Message) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if len(messages) > b.maxSize {
		messages = messages[len(messages)-b.maxSize:]
	}
	b.entries = append([]Message(nil), messages...)
	b.mu.Unlock()
}

func (b *MessageBuffer) Get(limit int) []Message {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > len(b.entries) {
		limit = len(b.entries)
	}
	start := len(b.entries) - limit
	out := make([]Message, limit)
	copy(out, b.entries[start:])
	return out
}

func (b *MessageBuffer) Clear() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.entries = nil
	b.mu.Unlock()
}

func (b *MessageBuffer) SetTap(tap func(Message)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.tap = tap
	b.mu.Unlock()
}
