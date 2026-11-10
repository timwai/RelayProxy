package p2p

import "time"

// Broadcast map changes to both maintenance loops. Each waiter reads the next
// channel under mu, so a transition cannot be lost or consumed by another loop.
func (m *Manager) wakeMaintenanceLocked() {
	close(m.maintenanceWake)
	m.maintenanceWake = make(chan struct{})
}

// maintainWhileActive leaves no timer armed when there is no work. Network
// checks include pending attempts and unexpired cooldowns so switching to a
// reachable network can wake a failed path. Map changes wake both loops.
func (m *Manager) maintainWhileActive(interval time.Duration, includePending bool, check func(time.Time)) {
	timer := time.NewTimer(interval)
	timer.Stop()
	defer timer.Stop()
	var tick <-chan time.Time
	for {
		m.mu.Lock()
		active := len(m.sessions) > 0
		if includePending {
			active = active || len(m.starting) > 0
			for _, failure := range m.cooldowns {
				active = active || time.Now().Before(failure.until)
			}
		}
		wake := m.maintenanceWake
		m.mu.Unlock()
		if active && tick == nil {
			timer.Reset(interval)
			tick = timer.C
		} else if !active && tick != nil {
			timer.Stop()
			tick = nil
		}
		select {
		case <-m.ctx.Done():
			return
		case <-wake:
		case now := <-tick:
			tick = nil
			check(now)
		}
	}
}
