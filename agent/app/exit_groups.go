package app

import (
	"relayproxy/agent/routing"
	"relayproxy/internal/exitgroup"
	"relayproxy/internal/proxy"
)

func (a *Agent) groupExitUsable(id string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if id == a.cfg.DeviceID {
		return false
	}
	for _, item := range a.proxyExits {
		if item.DeviceID == id {
			return item.Online
		}
	}
	return false
}

func (a *Agent) groupUpstreamDialer(id string) proxy.TunnelDialer {
	if exitgroup.IsID(id) {
		return routing.SelectedExitDialer{Routing: a.dialer}
	}
	return nil
}
