package session

import (
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type DeviceSession struct {
	DeviceID      string
	DeviceName    string
	OwnerUserID   string   // authenticated ownership snapshot; invalidated by authorization changes
	Mode          string   // "CLIENT", "EXIT", "BOTH"
	Capabilities  []string // authenticated transport/protocol features
	Grants        []string // server-approved product capabilities
	Transport     tunnel.TransportType
	Tunnel        tunnel.TunnelSession
	ControlStream tunnel.TunnelStream
	ConnectedAt   time.Time
	LastHeartbeat atomic.Int64 // Unix timestamp in seconds
	ActiveStreams atomic.Int64
	ActiveExitID  atomic.Pointer[string]
	BytesUp       atomic.Int64
	BytesDown     atomic.Int64
}

func (s *DeviceSession) IsExit() bool {
	for _, c := range s.Grants {
		if c == protocol.CapabilityProxyExit {
			return true
		}
	}
	return false
}

func (s *DeviceSession) TouchHeartbeat() {
	s.LastHeartbeat.Store(time.Now().Unix())
}

type Manager struct {
	mu              sync.RWMutex
	sessions        map[string]*DeviceSession
	exits           map[string]*DeviceSession
	exitsByOwner    map[string]map[string]*DeviceSession
	authorizationMu sync.Mutex
}

func NewManager() *Manager {
	return &Manager{
		sessions:     make(map[string]*DeviceSession),
		exits:        make(map[string]*DeviceSession),
		exitsByOwner: make(map[string]map[string]*DeviceSession),
	}
}

func (m *Manager) Register(sess *DeviceSession) {
	oldTunnel := m.register(sess)
	// Close old tunnel outside the locks: a transport implementation may wait
	// for its own shutdown path to finish.
	if oldTunnel != nil {
		_ = oldTunnel.Close()
	}
}

func (m *Manager) register(sess *DeviceSession) tunnel.TunnelSession {
	m.mu.Lock()
	var oldTunnel tunnel.TunnelSession
	if old, exists := m.sessions[sess.DeviceID]; exists {
		oldTunnel = old.Tunnel
		m.unindexExitLocked(old)
	}
	sess.TouchHeartbeat()
	m.sessions[sess.DeviceID] = sess
	m.indexExitLocked(sess)
	m.mu.Unlock()
	return oldTunnel
}

func (m *Manager) indexExitLocked(sess *DeviceSession) {
	if sess == nil || !sess.IsExit() {
		return
	}
	m.exits[sess.DeviceID] = sess
	if sess.OwnerUserID == "" {
		return
	}
	bucket := m.exitsByOwner[sess.OwnerUserID]
	if bucket == nil {
		bucket = make(map[string]*DeviceSession)
		m.exitsByOwner[sess.OwnerUserID] = bucket
	}
	bucket[sess.DeviceID] = sess
}

func (m *Manager) unindexExitLocked(sess *DeviceSession) {
	if sess == nil {
		return
	}
	delete(m.exits, sess.DeviceID)
	if sess.OwnerUserID == "" {
		return
	}
	if bucket := m.exitsByOwner[sess.OwnerUserID]; bucket != nil {
		delete(bucket, sess.DeviceID)
		if len(bucket) == 0 {
			delete(m.exitsByOwner, sess.OwnerUserID)
		}
	}
}

// removeLocked removes only the currently indexed generation.
func (m *Manager) removeLocked(deviceID string) *DeviceSession {
	sess := m.sessions[deviceID]
	if sess != nil {
		delete(m.sessions, deviceID)
		m.unindexExitLocked(sess)
	}
	return sess
}

// RegisterAuthenticated rechecks server approval under the same gate used by
// revocation. A verified handshake cannot register after approval is revoked.
func (m *Manager) RegisterAuthenticated(sess *DeviceSession, verify func() bool) bool {
	m.authorizationMu.Lock()
	if verify != nil && !verify() {
		m.authorizationMu.Unlock()
		return false
	}
	oldTunnel := m.register(sess)
	m.authorizationMu.Unlock()
	if oldTunnel != nil {
		_ = oldTunnel.Close()
	}
	return true
}

// ChangeDeviceAuthorization makes an approval mutation and its session
// invalidation indivisible with respect to authenticated registration.
func (m *Manager) ChangeDeviceAuthorization(deviceID string, revoke bool, change func() error) error {
	m.authorizationMu.Lock()
	if err := change(); err != nil {
		m.authorizationMu.Unlock()
		return err
	}
	var toClose tunnel.TunnelSession
	if revoke {
		// Authorization/ownership/grant changes invalidate the whole snapshot.
		// A new tunnel must authenticate again before its data-plane state is used.
		m.mu.Lock()
		if sess := m.removeLocked(deviceID); sess != nil {
			toClose = sess.Tunnel
		}
		m.mu.Unlock()
	}
	m.authorizationMu.Unlock()
	if toClose != nil {
		_ = toClose.Close()
	}
	return nil
}

func (m *Manager) UnregisterSession(sess *DeviceSession) bool {
	if sess == nil {
		return false
	}
	m.mu.Lock()
	var toClose tunnel.TunnelSession
	removed := false
	if current, exists := m.sessions[sess.DeviceID]; exists && current == sess {
		if removedSession := m.removeLocked(sess.DeviceID); removedSession != nil {
			toClose = removedSession.Tunnel
			removed = true
		}
	}
	m.mu.Unlock()
	if toClose != nil {
		_ = toClose.Close()
	}
	return removed
}

func (m *Manager) Unregister(deviceID string) {
	m.mu.Lock()
	var toClose tunnel.TunnelSession
	if sess := m.removeLocked(deviceID); sess != nil {
		toClose = sess.Tunnel
	}
	m.mu.Unlock()
	if toClose != nil {
		_ = toClose.Close()
	}
}

func (m *Manager) ReapStaleSessions(timeout time.Duration) {
	threshold := time.Now().Add(-timeout).Unix()
	var toClose []tunnel.TunnelSession

	m.mu.Lock()
	for id, sess := range m.sessions {
		if sess.LastHeartbeat.Load() < threshold {
			toClose = append(toClose, sess.Tunnel)
			m.removeLocked(id)
		}
	}
	m.mu.Unlock()

	for _, t := range toClose {
		if t != nil {
			_ = t.Close()
		}
	}
}

func (m *Manager) Get(deviceID string) (*DeviceSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sess, ok := m.sessions[deviceID]
	return sess, ok
}

func (m *Manager) List() []*DeviceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]*DeviceSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		res = append(res, s)
	}
	return res
}

func (m *Manager) GetExits() []*DeviceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make([]*DeviceSession, 0, len(m.exits))
	for _, s := range m.exits {
		res = append(res, s)
	}
	return res
}

// GetExitsForOwner avoids scanning non-exit sessions and lets the data plane
// use the ownership snapshot populated during authentication.
func (m *Manager) GetExitsForOwner(ownerUserID string) []*DeviceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if ownerUserID == "" {
		res := make([]*DeviceSession, 0, len(m.exits))
		for _, s := range m.exits {
			res = append(res, s)
		}
		return res
	}
	bucket := m.exitsByOwner[ownerUserID]
	res := make([]*DeviceSession, 0, len(bucket))
	for _, s := range bucket {
		res = append(res, s)
	}
	return res
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	toClose := make([]tunnel.TunnelSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		toClose = append(toClose, s.Tunnel)
	}
	m.sessions = make(map[string]*DeviceSession)
	m.exits = make(map[string]*DeviceSession)
	m.exitsByOwner = make(map[string]map[string]*DeviceSession)
	m.mu.Unlock()

	for _, t := range toClose {
		if t != nil {
			_ = t.Close()
		}
	}
}

// ExitNodeInfo is a DTO for returning exit node list to clients or web UI
type ExitNodeInfo struct {
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Transport  string `json:"transport"`
	Online     bool   `json:"online"`
}

func (m *Manager) GetExitNodesInfo() []ExitNodeInfo {
	exits := m.GetExits()
	res := make([]ExitNodeInfo, 0, len(exits))
	for _, e := range exits {
		res = append(res, ExitNodeInfo{
			DeviceID:   e.DeviceID,
			DeviceName: e.DeviceName,
			Transport:  string(e.Transport),
			Online:     true,
		})
	}
	return res
}
