package androidcore

// NetworkChanged keeps the local proxy listeners, DNS cache and resumable TCP
// registry alive while rebuilding sockets on Android's newly bound network.
// Android must finish process binding before reporting available=true.
func (c *Client) NetworkChanged(available bool) {
	if c == nil {
		return
	}
	c.mu.RLock()
	closed, manager := c.closed, c.manager
	c.mu.RUnlock()
	if !closed && manager != nil {
		manager.NetworkChanged(available)
	}
}
