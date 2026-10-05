package session

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type DeviceSession struct {
	SessionID      string
	DeviceID       string
	DeviceName     string
	Fingerprint    string   // verified installation public-key fingerprint
	OwnerUserID    string   // legacy authenticated ownership snapshot
	IdentityID     string   // server-resolved connection identity
	IdentityName   string   // display-only identity name snapshot
	PolicyRevision int64    // identity record revision at authentication time
	Mode           string   // "CONTROL", "CLIENT", "EXIT", "BOTH"
	Capabilities   []string // authenticated transport/protocol features
	Grants         []string // server-approved product capabilities
	// RuntimeCapabilities is nil for older clients. A non-nil slice is the
	// signed set of approved product capabilities that are currently running.
	RuntimeCapabilities []string
	Transport           tunnel.TransportType
	Tunnel              tunnel.TunnelSession
	ControlStream       tunnel.TunnelStream
	ConnectedAt         time.Time
	HeartbeatSec        int
	LastHeartbeat       atomic.Int64 // Unix timestamp in seconds
	ActiveStreams       atomic.Int64
	ActiveExitID        atomic.Pointer[string]
	BytesUp             atomic.Int64
	BytesDown           atomic.Int64
	Diagnostics         atomic.Pointer[DeviceDiagnostics]
}

type DeviceDiagnostics struct {
	ReceivedAt time.Time       `json:"receivedAt"`
	Payload    json.RawMessage `json:"payload"`
	Active     bool            `json:"-"`
}

func diagnosticPayloadActive(payload json.RawMessage) bool {
	var state struct {
		Status struct {
			ActiveStreams int64 `json:"activeStreams"`
		} `json:"status"`
		Connections []json.RawMessage `json:"connections"`
		Exit        *struct {
			ActiveTCP []json.RawMessage `json:"active_tcp"`
		} `json:"exit"`
	}
	if json.Unmarshal(payload, &state) != nil {
		return false
	}
	return state.Status.ActiveStreams > 0 || len(state.Connections) > 0 || state.Exit != nil && len(state.Exit.ActiveTCP) > 0
}

func (s *DeviceSession) SetDiagnostics(payload json.RawMessage) {
	if len(payload) == 0 || len(payload) > 256*1024 || !json.Valid(payload) {
		return
	}
	copy := append(json.RawMessage(nil), payload...)
	s.Diagnostics.Store(&DeviceDiagnostics{ReceivedAt: time.Now(), Payload: copy, Active: diagnosticPayloadActive(copy)})
}

func (s *DeviceSession) DiagnosticsSnapshot() *DeviceDiagnostics {
	current := s.Diagnostics.Load()
	if current == nil {
		return nil
	}
	return &DeviceDiagnostics{ReceivedAt: current.ReceivedAt, Payload: append(json.RawMessage(nil), current.Payload...), Active: current.Active}
}

func (s *DeviceSession) IsExit() bool {
	capabilities := s.Grants
	if s.RuntimeCapabilities != nil {
		capabilities = s.RuntimeCapabilities
	}
	for _, c := range capabilities {
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
	exitsByIdentity map[string]map[string]*DeviceSession
	authorizationMu sync.Mutex
}

func NewManager() *Manager {
	return &Manager{
		sessions:        make(map[string]*DeviceSession),
		exits:           make(map[string]*DeviceSession),
		exitsByOwner:    make(map[string]map[string]*DeviceSession),
		exitsByIdentity: make(map[string]map[string]*DeviceSession),
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
	if sess.OwnerUserID != "" {
		bucket := m.exitsByOwner[sess.OwnerUserID]
		if bucket == nil {
			bucket = make(map[string]*DeviceSession)
			m.exitsByOwner[sess.OwnerUserID] = bucket
		}
		bucket[sess.DeviceID] = sess
	}
	if sess.IdentityID != "" {
		bucket := m.exitsByIdentity[sess.IdentityID]
		if bucket == nil {
			bucket = make(map[string]*DeviceSession)
			m.exitsByIdentity[sess.IdentityID] = bucket
		}
		bucket[sess.DeviceID] = sess
	}
}

func (m *Manager) unindexExitLocked(sess *DeviceSession) {
	if sess == nil {
		return
	}
	delete(m.exits, sess.DeviceID)
	if sess.OwnerUserID != "" {
		if bucket := m.exitsByOwner[sess.OwnerUserID]; bucket != nil {
			delete(bucket, sess.DeviceID)
			if len(bucket) == 0 {
				delete(m.exitsByOwner, sess.OwnerUserID)
			}
		}
	}
	if sess.IdentityID != "" {
		if bucket := m.exitsByIdentity[sess.IdentityID]; bucket != nil {
			delete(bucket, sess.DeviceID)
			if len(bucket) == 0 {
				delete(m.exitsByIdentity, sess.IdentityID)
			}
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

// InvalidateIdentity closes every authenticated v5 session for an identity.
// Reconnecting refreshes the server-derived resource inventory and guarantees that existing
// Relay streams cannot outlive a revoked cross-identity grant.
func (m *Manager) InvalidateIdentity(identityID string) []string {
	if identityID == "" {
		return nil
	}
	m.authorizationMu.Lock()
	m.mu.Lock()
	deviceIDs := make([]string, 0)
	toClose := make([]tunnel.TunnelSession, 0)
	for deviceID, sess := range m.sessions {
		if sess == nil || sess.IdentityID != identityID {
			continue
		}
		if removed := m.removeLocked(deviceID); removed != nil {
			deviceIDs = append(deviceIDs, deviceID)
			if removed.Tunnel != nil {
				toClose = append(toClose, removed.Tunnel)
			}
		}
	}
	m.mu.Unlock()
	m.authorizationMu.Unlock()

	for _, transport := range toClose {
		_ = transport.Close()
	}
	return deviceIDs
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

// GetExitsForIdentity returns only exits authenticated under the same v5
// connection identity. Cross-identity grants are layered on top by policy.
func (m *Manager) GetExitsForIdentity(identityID string) []*DeviceSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if identityID == "" {
		return []*DeviceSession{}
	}
	bucket := m.exitsByIdentity[identityID]
	res := make([]*DeviceSession, 0, len(bucket))
	for _, s := range bucket {
		res = append(res, s)
	}
	return res
}

// UniqueExitForOwner is the allocation-free fast path used by auto-routing.
// The count lets callers distinguish no exit, exactly one, and ambiguity.
func (m *Manager) UniqueExitForOwner(ownerUserID string) (*DeviceSession, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var bucket map[string]*DeviceSession
	if ownerUserID == "" {
		bucket = m.exits
	} else {
		bucket = m.exitsByOwner[ownerUserID]
	}
	if len(bucket) != 1 {
		return nil, len(bucket)
	}
	for _, sess := range bucket {
		return sess, 1
	}
	return nil, 0
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
	m.exitsByIdentity = make(map[string]map[string]*DeviceSession)
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
