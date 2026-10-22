package androidcore

// MessageListener lets Android schedule message delivery only when a push
// arrives. Implementations must return promptly; UI work should be posted to
// the main thread. Callbacks run without the Client lock held.
type MessageListener interface {
	OnMessage()
}

// SetMessageListener replaces the push notification callback. Pending messages
// are signaled on registration, and Stop releases the callback reference.
func (c *Client) SetMessageListener(listener MessageListener) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.messageListener = listener
	pending := len(c.messages) > 0
	c.mu.Unlock()
	if pending && listener != nil {
		notifyMessageListener(listener)
	}
}

func notifyMessageListener(listener MessageListener) {
	// A Java callback failure must not interrupt the relay stream/receipt.
	defer func() { _ = recover() }()
	listener.OnMessage()
}
