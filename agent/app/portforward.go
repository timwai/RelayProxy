package app

import (
	"errors"

	"relayproxy/agent/portforward"
	"relayproxy/internal/protocol"
)

// PortForwardMappings returns the current in-memory mapping configuration and
// live path counters for the Agent UI.
func (a *Agent) PortForwardMappings() []portforward.Snapshot {
	if a == nil || a.portForwardMgr == nil {
		return nil
	}
	return a.portForwardMgr.List()
}

// AddPortForwardMapping registers a local mapping. During this first
// integration phase, the remote device must already be an approved Proxy Exit;
// its existing Exit ACL remains the target-side authorization boundary.
func (a *Agent) AddPortForwardMapping(mapping portforward.Mapping) error {
	if a == nil || a.closed.Load() {
		return errors.New("agent is closed")
	}
	if !a.handshakeOK.Load() {
		return errors.New("relay session is not approved")
	}
	if !a.portForwardTargetApproved(mapping.RemoteDeviceID) {
		return errors.New("remote target must be an approved Proxy Exit device")
	}
	if a.portForwardMgr == nil {
		return errors.New("port forward manager is unavailable")
	}
	return a.portForwardMgr.Add(mapping)
}

func (a *Agent) StartPortForwardMapping(id string) error {
	if a == nil || a.closed.Load() {
		return errors.New("agent is closed")
	}
	if a.portForwardMgr == nil {
		return errors.New("port forward manager is unavailable")
	}
	mapping, ok := a.portForwardMgr.Get(id)
	if !ok {
		return portforward.ErrMappingNotFound
	}
	if !a.handshakeOK.Load() || !a.portForwardTargetApproved(mapping.RemoteDeviceID) {
		return errors.New("remote target is no longer an approved Proxy Exit")
	}
	return a.portForwardMgr.StartMapping(id)
}

func (a *Agent) StopPortForwardMapping(id string) error {
	if a == nil || a.portForwardMgr == nil {
		return portforward.ErrMappingNotFound
	}
	return a.portForwardMgr.StopMapping(id)
}

func (a *Agent) RemovePortForwardMapping(id string) error {
	if a == nil || a.portForwardMgr == nil {
		return portforward.ErrMappingNotFound
	}
	return a.portForwardMgr.Remove(id)
}

func (a *Agent) portForwardTargetApproved(deviceID string) bool {
	if deviceID == "" || deviceID == protocol.ServerExitDeviceID {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, peer := range a.proxyExits {
		if peer.DeviceID == deviceID {
			return true
		}
	}
	return false
}
