package tunnel

// WakeReconnect expedites a pending retry without disturbing a healthy session.
// Desktop interface observers can call it when routing may have recovered but
// do not know whether the existing connection's selected path changed.
func (m *TunnelManager) WakeReconnect() {
	m.mu.RLock()
	canRetry := m.state != StateClosed && m.session == nil && m.networkAvailable
	m.mu.RUnlock()
	if canRetry {
		select {
		case m.networkWake <- struct{}{}:
		default:
		}
	}
}

// NetworkChanged is called after the platform has rebound new sockets to the
// selected network. Keep application listeners and resumable streams alive;
// only invalidate the transport created on the previous path.
func (m *TunnelManager) NetworkChanged(available bool) {
	m.mu.Lock()
	if m.state == StateClosed {
		m.mu.Unlock()
		return
	}
	m.networkAvailable = available
	m.metrics.NetworkChanges++
	cancel, sess := m.connectCancel, m.session
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sess != nil {
		m.MarkSessionLost(sess)
	}
	select {
	case m.networkWake <- struct{}{}:
	default:
	}
}
