// Package p2p implements the proxy direct path between a Client Agent and an
// Exit Agent. The Relay remains the authenticated control plane and fallback
// data path; a session is READY only after UDP punching and pinned QUIC succeed.
package p2p

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/acl"
	directp2p "relayproxy/internal/p2p"
	"relayproxy/internal/p2p/punch"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type ControlSender func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error)
type LocalDescription func() ([]protocol.P2PCandidate, string, error)
type EndpointFactory func() *Endpoint

type State string

const (
	StateDiscovering   State = "DISCOVERING"
	StateRendezvous    State = "RENDEZVOUS"
	StatePunching      State = "PUNCHING"
	StateQUICHandshake State = "QUIC_HANDSHAKE"
	StateReady         State = "READY"
	StateDegraded      State = "DEGRADED"
	StateCooldown      State = "COOLDOWN"
	StateClosed        State = "CLOSED"
)

type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	send   ControlSender
	local  LocalDescription
	lease  time.Duration

	endpointFactory      EndpointFactory
	punchTimeout         time.Duration
	keepAlive            time.Duration
	idleTimeout          time.Duration
	maxExitSessions      int
	lowPowerIdleTimeout  time.Duration
	lowPowerMaxSessions  int
	networkCheckInterval time.Duration
	networkSignature     func() string

	mu               sync.Mutex
	sessions         map[uint64]*Session
	pendingAnswers   map[uint64]protocol.P2PControlMessage
	starting         map[string]uint64
	cooldowns        map[string]failureState
	fallbacks        map[string]uint64
	networkSig       string
	ready            chan *Session
	closed           atomic.Bool
	attemptSeq       atomic.Uint64
	networkEpoch     atomic.Uint64
	powerConstrained atomic.Bool
}

type failureState struct {
	count  int
	until  time.Time
	reason string
}

type QUICManagerOptions struct {
	PunchTimeout         time.Duration
	KeepAlive            time.Duration
	IdleTimeout          time.Duration
	MaxExitSessions      int
	PortStart            int
	PortEnd              int
	UPnPEnabled          bool
	LowPowerIdleTimeout  time.Duration
	LowPowerMaxSessions  int
	NetworkCheckInterval time.Duration
	NetworkSignature     func() string
}

type Session struct {
	manager *Manager

	ID             uint64
	ClientDeviceID string
	ExitDeviceID   string
	Token          []byte

	ExpiresAt atomic.Int64

	mu               sync.RWMutex
	localCandidates  []protocol.P2PCandidate
	peerCandidates   []protocol.P2PCandidate
	peerFingerprint  string
	peerCapabilities []string
	relayPolicy      *acl.Policy
	state            State
	lastError        string
	endpoint         *Endpoint
	direct           *directp2p.QUICSession
	establishing     bool
	clientRole       bool
	lastRTTMs        int64
	lastBytesUp      uint64
	lastBytesDown    uint64
	lastUsed         atomic.Int64
	closed           chan struct{}
	closeOnce        sync.Once
}

type Snapshot struct {
	ID               uint64
	ClientDeviceID   string
	ExitDeviceID     string
	ExpiresAt        int64
	PeerCandidates   []protocol.P2PCandidate
	PeerFingerprint  string
	State            State
	Path             string
	Error            string
	RTTMs            int64
	CandidateSummary string
	FallbackCount    uint64
	BytesUp          uint64
	BytesDown        uint64
}

type PathStatus struct {
	SessionID        uint64
	ClientDeviceID   string
	ExitDeviceID     string
	ExpiresAt        int64
	State            State
	Path             string
	Error            string
	RTTMs            int64
	CandidateSummary string
	FallbackCount    uint64
	BytesUp          uint64
	BytesDown        uint64
}

// NewManager builds a signaling-only manager. It remains useful in tests and
// for capability negotiation before the direct transport is enabled.
func NewManager(parent context.Context, send ControlSender, local LocalDescription, lease time.Duration) *Manager {
	if parent == nil {
		parent = context.Background()
	}
	if lease < 15*time.Second {
		lease = 60 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	return &Manager{
		ctx: ctx, cancel: cancel, send: send, local: local, lease: lease,
		punchTimeout: 1200 * time.Millisecond, keepAlive: 10 * time.Second, idleTimeout: 120 * time.Second, maxExitSessions: 4,
		lowPowerIdleTimeout: 60 * time.Second, lowPowerMaxSessions: 1,
		networkCheckInterval: 10 * time.Second, networkSignature: CurrentNetworkSignature,
		sessions: make(map[uint64]*Session), pendingAnswers: make(map[uint64]protocol.P2PControlMessage), starting: make(map[string]uint64), cooldowns: make(map[string]failureState),
		fallbacks: make(map[string]uint64), ready: make(chan *Session, 1024),
	}
}

// NewQUICManager builds the production manager. Each Client/Exit pair receives
// a dedicated Endpoint so candidate discovery, punching and QUIC all use the
// exact same UDP socket and NAT mapping.
func NewQUICManager(parent context.Context, send ControlSender, rendezvous string, lease, punchTimeout time.Duration) *Manager {
	return NewQUICManagerWithOptions(parent, send, rendezvous, lease, QUICManagerOptions{PunchTimeout: punchTimeout})
}

func NewQUICManagerWithOptions(parent context.Context, send ControlSender, rendezvous string, lease time.Duration, options QUICManagerOptions) *Manager {
	m := NewManager(parent, send, nil, lease)
	if options.PunchTimeout > 0 {
		m.punchTimeout = options.PunchTimeout
	}
	if options.KeepAlive > 0 {
		m.keepAlive = options.KeepAlive
	}
	if options.IdleTimeout > 0 {
		m.idleTimeout = options.IdleTimeout
	}
	if options.MaxExitSessions > 0 {
		m.maxExitSessions = options.MaxExitSessions
	}
	if options.LowPowerIdleTimeout > 0 {
		m.lowPowerIdleTimeout = options.LowPowerIdleTimeout
	}
	if options.LowPowerMaxSessions > 0 {
		m.lowPowerMaxSessions = options.LowPowerMaxSessions
	}
	if options.NetworkCheckInterval > 0 {
		m.networkCheckInterval = options.NetworkCheckInterval
	}
	if options.NetworkSignature != nil {
		m.networkSignature = options.NetworkSignature
	}
	if m.networkSignature != nil {
		m.networkSig = m.networkSignature()
	}
	m.endpointFactory = func() *Endpoint {
		return NewEndpointWithPortRangeAndUPnP(rendezvous, options.PortStart, options.PortEnd, options.UPnPEnabled)
	}
	go m.reapIdleLoop()
	go m.watchNetworkLoop()
	return m
}

func (m *Manager) Close() error {
	if m == nil || m.closed.Swap(true) {
		return nil
	}
	m.cancel()
	m.mu.Lock()
	items := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		items = append(items, item)
	}
	m.sessions = make(map[uint64]*Session)
	m.pendingAnswers = make(map[uint64]protocol.P2PControlMessage)
	m.mu.Unlock()
	for _, item := range items {
		item.closeLocal()
	}
	return nil
}

// ReadySessions emits sessions after UDP punching, fingerprint verification and
// the direct QUIC handshake have all succeeded.
func (m *Manager) ReadySessions() <-chan *Session {
	if m == nil {
		return nil
	}
	return m.ready
}

// PrewarmClient prepares a direct path only when the device is not in a
// power-constrained mode. Reactive attempts should use EnsureClient instead.
func (m *Manager) PrewarmClient(exitDeviceID string) {
	if m == nil || m.powerConstrained.Load() {
		return
	}
	m.EnsureClient(exitDeviceID)
}

// SetPowerConstrained enables the battery-aware profile used by mobile clients.
// Existing active streams are preserved; idle sessions are reaped by the normal
// lifecycle loop and new QUIC paths stop sending periodic keepalives.
func (m *Manager) SetPowerConstrained(constrained bool) {
	if m == nil || m.closed.Load() {
		return
	}
	previous := m.powerConstrained.Swap(constrained)
	if constrained && !previous {
		m.enforceClientLimit(0)
	}
}

func (m *Manager) PowerConstrained() bool {
	return m != nil && m.powerConstrained.Load()
}

// EnsureClient starts at most one in-flight direct-path attempt for an Exit.
// Callers should not wait for it: the current connection can use Relay while
// the P2P path is prepared for subsequent flows.
func (m *Manager) EnsureClient(exitDeviceID string) {
	// The Relay server exit is local to the control-plane process and has no
	// peer Agent to punch or authenticate. Keep it on the Relay tunnel without
	// creating a doomed P2P attempt or cooldown entry.
	if m == nil || exitDeviceID == "" || exitDeviceID == protocol.ServerExitDeviceID || m.closed.Load() {
		return
	}
	m.mu.Lock()
	if failure, exists := m.cooldowns[exitDeviceID]; exists && time.Now().Before(failure.until) {
		m.mu.Unlock()
		return
	}
	if _, exists := m.starting[exitDeviceID]; exists {
		m.mu.Unlock()
		return
	}
	for _, item := range m.sessions {
		if item.ExitDeviceID != exitDeviceID {
			continue
		}
		switch item.State() {
		case StateDiscovering, StateRendezvous, StatePunching, StateQUICHandshake, StateReady:
			m.mu.Unlock()
			return
		}
	}
	attemptID := m.attemptSeq.Add(1)
	m.starting[exitDeviceID] = attemptID
	epoch := m.networkEpoch.Load()
	m.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
		defer cancel()
		item, err := m.StartClient(ctx, exitDeviceID)
		if epoch != m.networkEpoch.Load() {
			if item != nil {
				m.removeFailedSession(item, "network_changed")
			}
		} else if err != nil {
			m.recordFailure(exitDeviceID, err.Error())
		}
		m.mu.Lock()
		if current, ok := m.starting[exitDeviceID]; ok && current == attemptID {
			delete(m.starting, exitDeviceID)
		}
		m.mu.Unlock()
	}()
}

func (m *Manager) StartClient(ctx context.Context, exitDeviceID string) (*Session, error) {
	if m == nil || m.send == nil || (m.local == nil && m.endpointFactory == nil) {
		return nil, errors.New("proxy P2P manager is not configured")
	}
	if m.closed.Load() {
		return nil, net.ErrClosed
	}
	if exitDeviceID == "" {
		return nil, errors.New("exit device id is required")
	}
	m.pruneExit(exitDeviceID)

	candidates, fingerprint, endpoint, err := m.prepareLocal(ctx)
	if err != nil {
		return nil, err
	}
	response, err := m.send(ctx, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectRequest, ExitDeviceID: exitDeviceID,
		Candidates: append([]protocol.P2PCandidate(nil), candidates...), CertFingerprint: fingerprint,
	})
	if err != nil {
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return nil, err
	}
	if response.Type == protocol.P2PControlError {
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return nil, fmt.Errorf("P2P connect rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}
	if response.Type != protocol.P2PControlLeaseAck || response.SessionID == 0 || len(response.SessionToken) < 16 {
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return nil, errors.New("P2P coordinator returned an incomplete session")
	}
	item := m.newSessionWithEndpoint(
		response.SessionID, response.ClientDeviceID, response.ExitDeviceID,
		response.SessionToken, response.LeaseExpiresAt, endpoint,
	)
	if item == nil {
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return nil, net.ErrClosed
	}
	item.mu.Lock()
	item.clientRole = true
	item.localCandidates = append([]protocol.P2PCandidate(nil), candidates...)
	item.lastUsed.Store(time.Now().UnixMilli())
	item.mu.Unlock()
	item.setPeerCapabilities(response.PeerCapabilities)
	// The Exit can answer before connect_request returns and registers this
	// session. Consume any early answer only after client-side state is ready.
	m.mu.Lock()
	pending, hasPending := m.pendingAnswers[item.ID]
	delete(m.pendingAnswers, item.ID)
	m.mu.Unlock()
	if hasPending {
		m.applyConnectAnswer(item, pending)
	}
	return item, nil
}

func (m *Manager) applyConnectAnswer(item *Session, message protocol.P2PControlMessage) {
	if item == nil || !item.matchesToken(message.SessionToken) {
		return
	}
	item.setPeer(message.Candidates, message.PeerFingerprint)
	item.setPeerCapabilities(message.PeerCapabilities)
	if message.LeaseExpiresAt > 0 {
		item.ExpiresAt.Store(message.LeaseExpiresAt)
	}
	if item.hasEndpoint() {
		go item.establish(true)
	}
}

func (m *Manager) HandleControl(message protocol.P2PControlMessage) {
	if m == nil || m.closed.Load() {
		return
	}
	switch message.Type {
	case protocol.P2PControlConnectOffer:
		if message.SessionID == 0 || len(message.SessionToken) < 16 || message.ExitDeviceID == "" {
			return
		}
		go m.handleOffer(message)
	case protocol.P2PControlConnectAnswer:
		if message.SessionID == 0 || len(message.SessionToken) < 16 {
			return
		}
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		if item == nil {
			// Server push streams and the initiating request use different
			// Relay streams; their delivery order is not guaranteed.
			if len(m.pendingAnswers) >= 128 {
				for id := range m.pendingAnswers {
					delete(m.pendingAnswers, id)
					break
				}
			}
			message.SessionToken = append([]byte(nil), message.SessionToken...)
			message.Candidates = append([]protocol.P2PCandidate(nil), message.Candidates...)
			m.pendingAnswers[message.SessionID] = message
		}
		m.mu.Unlock()
		if item != nil {
			m.applyConnectAnswer(item, message)
		}
	case protocol.P2PControlCandidateUpdate:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil && item.matchesToken(message.SessionToken) {
			item.setPeer(message.Candidates, "")
			if message.LeaseExpiresAt > 0 {
				item.ExpiresAt.Store(message.LeaseExpiresAt)
			}
		}
	case protocol.P2PControlLeaseAck:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil && message.LeaseExpiresAt > 0 {
			item.ExpiresAt.Store(message.LeaseExpiresAt)
		}
	case protocol.P2PControlClose, protocol.P2PControlRevoke:
		m.remove(message.SessionID)
	}
}

func (m *Manager) handleOffer(message protocol.P2PControlMessage) {
	if m.send == nil || m.closed.Load() || (m.local == nil && m.endpointFactory == nil) {
		return
	}
	candidates, fingerprint, endpoint, err := m.prepareLocal(m.ctx)
	if err != nil {
		return
	}
	item := m.newSessionWithEndpoint(
		message.SessionID, message.ClientDeviceID, message.ExitDeviceID,
		message.SessionToken, message.LeaseExpiresAt, endpoint,
	)
	if item == nil {
		if endpoint != nil {
			_ = endpoint.Close()
		}
		return
	}
	item.mu.Lock()
	item.localCandidates = append([]protocol.P2PCandidate(nil), candidates...)
	item.mu.Unlock()
	item.setPeer(message.Candidates, message.PeerFingerprint)
	item.setPeerCapabilities(message.PeerCapabilities)
	policy := message.RelayPolicy
	if m.endpointFactory != nil {
		var policyErr error
		policy, policyErr = validateRelayPolicy(policy)
		if policyErr != nil {
			m.remove(message.SessionID)
			return
		}
	}
	item.setRelayPolicy(policy)
	item.setState(StateRendezvous, "")

	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	response, err := m.send(ctx, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectAnswer, SessionID: message.SessionID,
		ClientDeviceID: message.ClientDeviceID, ExitDeviceID: message.ExitDeviceID,
		SessionToken: append([]byte(nil), message.SessionToken...),
		Candidates:   append([]protocol.P2PCandidate(nil), candidates...), CertFingerprint: fingerprint,
	})
	cancel()
	if err != nil || response.Type == protocol.P2PControlError {
		m.remove(message.SessionID)
		return
	}
	if response.Type == protocol.P2PControlLeaseAck && response.LeaseExpiresAt > 0 {
		item.ExpiresAt.Store(response.LeaseExpiresAt)
	}
	if item.hasEndpoint() {
		go item.establish(false)
	}
}

func (m *Manager) prepareLocal(ctx context.Context) ([]protocol.P2PCandidate, string, *Endpoint, error) {
	if m.endpointFactory != nil {
		endpoint := m.endpointFactory()
		if endpoint == nil {
			return nil, "", nil, errors.New("P2P endpoint factory returned nil")
		}
		if err := endpoint.Start(ctx); err != nil {
			_ = endpoint.Close()
			return nil, "", nil, err
		}
		candidates, fingerprint, err := endpoint.Description()
		if err != nil {
			_ = endpoint.Close()
			return nil, "", nil, err
		}
		return candidates, fingerprint, endpoint, nil
	}
	if m.local == nil {
		return nil, "", nil, errors.New("P2P local description is unavailable")
	}
	candidates, fingerprint, err := m.local()
	if err != nil {
		return nil, "", nil, err
	}
	if len(candidates) == 0 || fingerprint == "" {
		return nil, "", nil, errors.New("P2P local description is incomplete")
	}
	return candidates, fingerprint, nil, nil
}

func (m *Manager) Session(id uint64) (*Session, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.sessions[id]
	return item, ok
}

// ReadyForExit returns a usable direct tunnel only after punching, the QUIC
// handshake and certificate fingerprint verification all succeeded.
func (m *Manager) ReadyForExit(exitDeviceID string) (tunnel.TunnelSession, bool) {
	if m == nil || exitDeviceID == "" {
		return nil, false
	}
	m.mu.Lock()
	items := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		if item.ExitDeviceID == exitDeviceID {
			items = append(items, item)
		}
	}
	m.mu.Unlock()
	for _, item := range items {
		if direct, ok := item.Tunnel(); ok {
			item.lastUsed.Store(time.Now().UnixMilli())
			return direct, true
		}
	}
	return nil, false
}

// PathStatus returns a sanitized snapshot for UI/diagnostics. It intentionally
// excludes candidates, certificate fingerprints and session tokens.
func (m *Manager) PathStatus(exitDeviceID string) (PathStatus, bool) {
	if m == nil {
		return PathStatus{}, false
	}
	m.mu.Lock()
	items := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		if exitDeviceID == "" || item.ExitDeviceID == exitDeviceID {
			items = append(items, item)
		}
	}
	m.mu.Unlock()

	var best PathStatus
	bestRank := -1
	for _, item := range items {
		snapshot := item.Snapshot()
		rank := pathStateRank(snapshot.State)
		if rank < bestRank || (rank == bestRank && snapshot.ExpiresAt <= best.ExpiresAt) {
			continue
		}
		bestRank = rank
		best = PathStatus{
			SessionID: snapshot.ID, ClientDeviceID: snapshot.ClientDeviceID,
			ExitDeviceID: snapshot.ExitDeviceID, ExpiresAt: snapshot.ExpiresAt,
			State: snapshot.State, Path: snapshot.Path, Error: snapshot.Error,
			RTTMs: snapshot.RTTMs, CandidateSummary: snapshot.CandidateSummary,
			FallbackCount: snapshot.FallbackCount, BytesUp: snapshot.BytesUp, BytesDown: snapshot.BytesDown,
		}
	}
	m.mu.Lock()
	failure, cooling := m.cooldowns[exitDeviceID]
	m.mu.Unlock()
	if cooling && time.Now().Before(failure.until) && (bestRank < 0 || bestRank <= pathStateRank(StateDegraded)) {
		m.mu.Lock()
		fallbacks := m.fallbacks[exitDeviceID]
		m.mu.Unlock()
		return PathStatus{
			ExitDeviceID: exitDeviceID, State: StateCooldown,
			Error: failure.reason, FallbackCount: fallbacks,
		}, true
	}
	return best, bestRank >= 0
}

func pathStateRank(state State) int {
	switch state {
	case StateReady:
		return 6
	case StateQUICHandshake:
		return 5
	case StatePunching:
		return 4
	case StateRendezvous:
		return 3
	case StateDiscovering:
		return 2
	case StateDegraded:
		return 2
	case StateCooldown:
		return 1
	case StateClosed:
		return 0
	default:
		return -1
	}
}

func (m *Manager) newSession(id uint64, clientDeviceID, exitDeviceID string, token []byte, expires int64) *Session {
	return m.newSessionWithEndpoint(id, clientDeviceID, exitDeviceID, token, expires, nil)
}

func (m *Manager) newSessionWithEndpoint(id uint64, clientDeviceID, exitDeviceID string, token []byte, expires int64, endpoint *Endpoint) *Session {
	if id == 0 || len(token) < 16 {
		return nil
	}
	if expires == 0 {
		expires = time.Now().Add(m.lease).UnixMilli()
	}
	item := &Session{
		manager: m, ID: id, ClientDeviceID: clientDeviceID, ExitDeviceID: exitDeviceID,
		Token: append([]byte(nil), token...), state: StateRendezvous, endpoint: endpoint,
		closed: make(chan struct{}),
	}
	item.ExpiresAt.Store(expires)
	item.lastUsed.Store(time.Now().UnixMilli())
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return nil
	}
	old := m.sessions[id]
	m.sessions[id] = item
	m.mu.Unlock()
	if old != nil {
		old.closeLocal()
	}
	go item.renewLoop()
	if endpoint != nil {
		go item.expireUnanswered(15 * time.Second)
		go item.watchLocalCandidates()
	}
	return item
}

// expireUnanswered prevents a missing or reordered signaling message from
// holding a lease alive indefinitely via renewLoop.
func (s *Session) expireUnanswered(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.closed:
		return
	case <-s.manager.ctx.Done():
		return
	case <-timer.C:
	}
	s.mu.RLock()
	unanswered := s.state == StateRendezvous && (s.peerFingerprint == "" || len(s.peerCandidates) == 0)
	s.mu.RUnlock()
	if unanswered {
		s.failDirect(errors.New("P2P connect answer timeout"))
	}
}

func (m *Manager) remove(id uint64) {
	m.mu.Lock()
	item := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if item != nil {
		item.closeLocal()
	}
}

func (m *Manager) pruneExit(exitDeviceID string) {
	m.mu.Lock()
	var stale []*Session
	for id, item := range m.sessions {
		if item.ExitDeviceID != exitDeviceID {
			continue
		}
		state := item.State()
		if state == StateDegraded || state == StateClosed {
			delete(m.sessions, id)
			stale = append(stale, item)
		}
	}
	m.mu.Unlock()
	for _, item := range stale {
		item.closeLocal()
	}
}

func (s *Session) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	var fallbackCount uint64
	if s.manager != nil {
		s.manager.mu.Lock()
		fallbackCount = s.manager.fallbacks[s.ExitDeviceID]
		s.manager.mu.Unlock()
	}
	s.mu.RLock()
	path := ""
	stats := directp2p.QUICStats{
		RTT:       time.Duration(s.lastRTTMs) * time.Millisecond,
		BytesSent: s.lastBytesUp, BytesReceived: s.lastBytesDown,
	}
	if s.state == StateReady {
		path = protocol.P2PPathDirectQUIC
		if s.direct != nil {
			stats = s.direct.Stats()
		}
	}
	local := append([]protocol.P2PCandidate(nil), s.localCandidates...)
	peer := append([]protocol.P2PCandidate(nil), s.peerCandidates...)
	candidateSummary := summarizeCandidates(local, peer)
	if s.endpoint != nil {
		state, _ := s.endpoint.UPnPStatus()
		candidateSummary += " upnp=" + strings.ToLower(state)
	}
	out := Snapshot{
		ID: s.ID, ClientDeviceID: s.ClientDeviceID, ExitDeviceID: s.ExitDeviceID,
		ExpiresAt: s.ExpiresAt.Load(), PeerCandidates: peer,
		PeerFingerprint: s.peerFingerprint, State: s.state, Path: path, Error: s.lastError,
		RTTMs: stats.RTT.Milliseconds(), CandidateSummary: candidateSummary,
		FallbackCount: fallbackCount, BytesUp: stats.BytesSent, BytesDown: stats.BytesReceived,
	}
	s.mu.RUnlock()
	return out
}

func (s *Session) State() State {
	if s == nil {
		return StateClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Session) Tunnel() (tunnel.TunnelSession, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state != StateReady || s.direct == nil || s.direct.QUICSession == nil {
		return nil, false
	}
	return s.direct.QUICSession, true
}

func (s *Session) setPeer(candidates []protocol.P2PCandidate, fingerprint string) {
	s.mu.Lock()
	s.peerCandidates = append([]protocol.P2PCandidate(nil), candidates...)
	if fingerprint != "" {
		s.peerFingerprint = fingerprint
	}
	s.mu.Unlock()
}

func (s *Session) setPeerCapabilities(values []string) {
	if s == nil {
		return
	}
	caps := make([]string, 0, 1)
	for _, value := range values {
		if value == protocol.CapabilityProxyStreamResume {
			caps = append(caps, value)
			break
		}
	}
	s.mu.Lock()
	s.peerCapabilities = caps
	s.mu.Unlock()
}

func (s *Session) peerCapabilitiesSnapshot() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.peerCapabilities...)
}

func (s *Session) setRelayPolicy(policy *acl.Policy) {
	s.mu.Lock()
	if policy == nil {
		s.relayPolicy = nil
	} else {
		copy := *policy
		copy.Rules = slices.Clone(policy.Rules)
		copy.AccessHosts = slices.Clone(policy.AccessHosts)
		copy.AccessCIDRs = slices.Clone(policy.AccessCIDRs)
		s.relayPolicy = &copy
	}
	s.mu.Unlock()
}

func (s *Session) RelayPolicy() *acl.Policy {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.relayPolicy == nil {
		return nil
	}
	copy := *s.relayPolicy
	copy.Rules = slices.Clone(s.relayPolicy.Rules)
	copy.AccessHosts = slices.Clone(s.relayPolicy.AccessHosts)
	copy.AccessCIDRs = slices.Clone(s.relayPolicy.AccessCIDRs)
	return &copy
}

func (s *Session) setState(state State, reason string) {
	s.mu.Lock()
	s.state = state
	s.lastError = reason
	s.mu.Unlock()
}

func (s *Session) hasEndpoint() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endpoint != nil
}

func (s *Session) matchesToken(token []byte) bool {
	if s == nil || len(token) != len(s.Token) {
		return false
	}
	return subtle.ConstantTimeCompare(token, s.Token) == 1
}

func (s *Session) establish(clientRole bool) {
	if s == nil || s.manager == nil {
		return
	}
	s.mu.Lock()
	if s.state == StateClosed || s.establishing || s.direct != nil || s.endpoint == nil {
		s.mu.Unlock()
		return
	}
	if len(s.peerCandidates) == 0 || s.peerFingerprint == "" {
		s.mu.Unlock()
		return
	}
	s.establishing = true
	s.state = StatePunching
	s.lastError = ""
	endpoint := s.endpoint
	candidates := append([]protocol.P2PCandidate(nil), s.peerCandidates...)
	fingerprint := s.peerFingerprint
	s.mu.Unlock()

	conn, err := endpoint.UDPConn()
	if err != nil {
		s.failDirect(err)
		return
	}
	identity, err := endpoint.Identity()
	if err != nil {
		s.failDirect(err)
		return
	}
	timeout := s.manager.punchTimeout
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(s.manager.ctx, timeout+8*time.Second)
	defer cancel()
	result, err := punch.Punch(ctx, conn, candidates, s.ID, s.Token, timeout)
	if err != nil {
		s.failDirect(err)
		return
	}

	s.setState(StateQUICHandshake, "")
	var direct *directp2p.QUICSession
	quicOptions := s.manager.directQUICOptions()
	if clientRole {
		direct, err = directp2p.DialQUIC(ctx, result.Conn, result.RemoteAddr, identity, fingerprint, quicOptions)
	} else {
		direct, err = directp2p.AcceptQUIC(ctx, result.Conn, identity, fingerprint, quicOptions)
	}
	if err != nil {
		s.failDirect(err)
		return
	}
	if direct.QUICSession != nil {
		// Native datagrams are a property of the direct QUIC transport itself.
		// Overlay capabilities are server-authoritative and are added without
		// clearing the already-negotiated datagram capability.
		caps := append([]string{protocol.UDPModeDatagram}, s.peerCapabilitiesSnapshot()...)
		tunnel.SetPeerCapabilities(direct.QUICSession, caps)
	}

	s.mu.Lock()
	if s.state == StateClosed {
		s.establishing = false
		s.mu.Unlock()
		_ = direct.Close()
		return
	}
	s.direct = direct
	s.establishing = false
	s.clientRole = clientRole
	s.lastUsed.Store(time.Now().UnixMilli())
	s.state = StateReady
	s.lastError = ""
	s.mu.Unlock()
	if clientRole {
		s.manager.resetFailure(s.ExitDeviceID)
		s.manager.enforceClientLimit(s.ID)
	}
	s.reportPath(protocol.P2PPathDirectQUIC, "")
	select {
	case s.manager.ready <- s:
	default:
		s.mu.Lock()
		if s.direct == direct && s.state != StateClosed {
			s.direct = nil
			s.state = StateDegraded
			s.lastError = "P2P ready queue is full"
		}
		endpoint := s.endpoint
		s.endpoint = nil
		s.mu.Unlock()
		_ = direct.Close()
		if endpoint != nil {
			_ = endpoint.Close()
		}
		s.reportPath("", "ready_queue_full")
		return
	}
	go s.watchDirect(direct)
}

func (s *Session) failDirect(err error) {
	reason := "direct path failed"
	if err != nil {
		reason = err.Error()
	}
	s.mu.Lock()
	if s.state != StateClosed {
		s.state = StateDegraded
		s.lastError = reason
	}
	s.establishing = false
	clientRole := s.clientRole
	direct := s.direct
	endpoint := s.endpoint
	s.direct = nil
	s.endpoint = nil
	if direct != nil {
		stats := direct.Stats()
		s.lastRTTMs = stats.RTT.Milliseconds()
		s.lastBytesUp = stats.BytesSent
		s.lastBytesDown = stats.BytesReceived
	}
	s.mu.Unlock()
	if direct != nil {
		_ = direct.Close()
	}
	if endpoint != nil {
		_ = endpoint.Close()
	}
	if clientRole {
		s.manager.recordFailure(s.ExitDeviceID, reason)
	}
	s.reportPath("", reason)
	s.manager.removeFailedSession(s, reason)
}

func (s *Session) watchDirect(direct *directp2p.QUICSession) {
	if direct == nil {
		return
	}
	select {
	case <-s.closed:
		return
	case <-s.manager.ctx.Done():
		return
	case <-direct.Done():
	}
	stats := direct.Stats()
	s.mu.Lock()
	if s.direct != direct || s.state == StateClosed {
		s.mu.Unlock()
		return
	}
	s.lastRTTMs = stats.RTT.Milliseconds()
	s.lastBytesUp = stats.BytesSent
	s.lastBytesDown = stats.BytesReceived
	s.direct = nil
	s.state = StateDegraded
	s.lastError = "P2P QUIC session closed"
	clientRole := s.clientRole
	endpoint := s.endpoint
	s.endpoint = nil
	s.mu.Unlock()
	_ = direct.Close()
	if endpoint != nil {
		_ = endpoint.Close()
	}
	if clientRole {
		s.manager.recordFailure(s.ExitDeviceID, "P2P QUIC session closed")
	}
	s.reportPath("", "quic_session_closed")
	s.manager.removeFailedSession(s, "quic_session_closed")
}

func (s *Session) reportPath(path, reason string) {
	if s == nil || s.manager == nil || s.manager.send == nil {
		return
	}
	snapshot := s.Snapshot()
	go func() {
		ctx, cancel := context.WithTimeout(s.manager.ctx, 3*time.Second)
		defer cancel()
		_, _ = s.manager.send(ctx, protocol.P2PControlMessage{
			Type: protocol.P2PControlPathReport, SessionID: s.ID,
			ClientDeviceID: s.ClientDeviceID, ExitDeviceID: s.ExitDeviceID,
			SessionToken: append([]byte(nil), s.Token...), Path: path, Reason: reason,
			RTTMs: snapshot.RTTMs, CandidateSummary: snapshot.CandidateSummary,
			FallbackCount: snapshot.FallbackCount, BytesUp: snapshot.BytesUp, BytesDown: snapshot.BytesDown,
		})
	}()
}

// watchLocalCandidates propagates UPnP renewal failures and remaps to the
// peer, rather than leaving expired public UDP addresses in the rendezvous
// candidate list. The endpoint owns and coalesces the latest candidate state.
func (s *Session) watchLocalCandidates() {
	s.watchLocalCandidatesWithRetry(15 * time.Second)
}

func (s *Session) watchLocalCandidatesWithRetry(retry time.Duration) {
	if s == nil || s.endpoint == nil || s.manager == nil {
		return
	}
	changes := s.endpoint.CandidateChanges()
	if retry <= 0 {
		retry = 15 * time.Second
	}
	ticker := time.NewTicker(retry)
	defer ticker.Stop()
	var latest []protocol.P2PCandidate
	dirty := false
	for {
		select {
		case <-s.closed:
			return
		case <-s.manager.ctx.Done():
			return
		case candidates := <-changes:
			latest = append([]protocol.P2PCandidate(nil), candidates...)
			s.mu.Lock()
			if s.state == StateClosed {
				s.mu.Unlock()
				return
			}
			s.localCandidates = append([]protocol.P2PCandidate(nil), latest...)
			s.mu.Unlock()
			dirty = true
		case <-ticker.C:
			if !dirty {
				continue
			}
		}
		if !dirty || s.manager.send == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(s.manager.ctx, 5*time.Second)
		response, err := s.manager.send(ctx, protocol.P2PControlMessage{
			Type: protocol.P2PControlCandidateUpdate, SessionID: s.ID,
			ClientDeviceID: s.ClientDeviceID, ExitDeviceID: s.ExitDeviceID,
			SessionToken: append([]byte(nil), s.Token...),
			Candidates:   append([]protocol.P2PCandidate(nil), latest...),
		})
		cancel()
		if err == nil && response.Type == protocol.P2PControlLeaseAck {
			dirty = false
			if response.LeaseExpiresAt > 0 {
				s.ExpiresAt.Store(response.LeaseExpiresAt)
			}
		}
	}
}

func (s *Session) renewLoop() {
	if s == nil || s.manager == nil {
		return
	}
	interval := s.manager.lease / 3
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-s.manager.ctx.Done():
			return
		case <-ticker.C:
			if s.manager.send == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(s.manager.ctx, 5*time.Second)
			response, err := s.manager.send(ctx, protocol.P2PControlMessage{
				Type: protocol.P2PControlLeaseRenew, SessionID: s.ID,
				ClientDeviceID: s.ClientDeviceID, ExitDeviceID: s.ExitDeviceID,
				SessionToken: append([]byte(nil), s.Token...),
			})
			cancel()
			if err != nil || response.Type == protocol.P2PControlError {
				s.manager.remove(s.ID)
				return
			}
			if response.Type == protocol.P2PControlLeaseAck && response.LeaseExpiresAt > 0 {
				s.ExpiresAt.Store(response.LeaseExpiresAt)
			}
			if s.State() == StateReady {
				s.reportPath(protocol.P2PPathDirectQUIC, "")
			}
		}
	}
}

func (s *Session) closeLocal() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = StateClosed
		s.establishing = false
		direct := s.direct
		endpoint := s.endpoint
		s.direct = nil
		s.endpoint = nil
		s.mu.Unlock()
		close(s.closed)
		if direct != nil {
			_ = direct.Close()
		}
		if endpoint != nil {
			_ = endpoint.Close()
		}
	})
}

func validateRelayPolicy(policy *acl.Policy) (*acl.Policy, error) {
	if policy == nil || policy.Fingerprint == "" {
		return nil, errors.New("P2P connect offer is missing the server Relay ACL")
	}
	checker, err := acl.NewChecker(*policy)
	if err != nil {
		return nil, fmt.Errorf("invalid P2P Relay ACL: %w", err)
	}
	normalized := checker.Policy()
	if normalized.Fingerprint != policy.Fingerprint {
		return nil, errors.New("P2P Relay ACL fingerprint mismatch")
	}
	copy := normalized
	copy.Rules = slices.Clone(normalized.Rules)
	copy.AccessHosts = slices.Clone(normalized.AccessHosts)
	copy.AccessCIDRs = slices.Clone(normalized.AccessCIDRs)
	return &copy, nil
}

// FailReadyForExit invalidates a client-side READY path after the dialer
// observes a transport-level stream establishment failure. Business/ACL errors
// are filtered by the dialer and never reach this method.
func (m *Manager) FailReadyForExit(exitDeviceID, reason string) {
	if m == nil || exitDeviceID == "" {
		return
	}
	if reason == "" {
		reason = "P2P direct stream failed"
	}
	m.mu.Lock()
	items := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		item.mu.RLock()
		clientRole, state := item.clientRole, item.state
		item.mu.RUnlock()
		if item.ExitDeviceID == exitDeviceID && clientRole && state == StateReady {
			items = append(items, item)
		}
	}
	m.mu.Unlock()
	for _, item := range items {
		item.failDirect(errors.New(reason))
	}
}

func (m *Manager) NoteFallback(exitDeviceID string) {
	if m == nil || exitDeviceID == "" {
		return
	}
	m.mu.Lock()
	m.fallbacks[exitDeviceID]++
	items := make([]*Session, 0, len(m.sessions))
	for _, candidate := range m.sessions {
		if candidate.ExitDeviceID == exitDeviceID {
			items = append(items, candidate)
		}
	}
	m.mu.Unlock()
	for _, item := range items {
		if item.State() == StateReady {
			item.reportPath(protocol.P2PPathDirectQUIC, "")
			return
		}
	}
}

func summarizeCandidates(local, peer []protocol.P2PCandidate) string {
	count := func(items []protocol.P2PCandidate) (lan, reflexive int) {
		for _, item := range items {
			switch item.Type {
			case "lan":
				lan++
			case "reflexive":
				reflexive++
			}
		}
		return
	}
	localLAN, localReflexive := count(local)
	peerLAN, peerReflexive := count(peer)
	return fmt.Sprintf("local:lan=%d,reflexive=%d;peer:lan=%d,reflexive=%d", localLAN, localReflexive, peerLAN, peerReflexive)
}

func (m *Manager) watchNetworkLoop() {
	if m == nil || m.networkSignature == nil || m.networkCheckInterval <= 0 {
		return
	}
	ticker := time.NewTicker(m.networkCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			current := m.networkSignature()
			m.mu.Lock()
			previous := m.networkSig
			if previous == "" {
				m.networkSig = current
				m.mu.Unlock()
				continue
			}
			changed := current != "" && current != previous
			if changed {
				m.networkSig = current
			}
			m.mu.Unlock()
			if changed {
				m.invalidateNetwork()
			}
		}
	}
}

func (m *Manager) invalidateNetwork() {
	if m == nil || m.closed.Load() {
		return
	}
	m.mu.Lock()
	m.networkEpoch.Add(1)
	items := make([]*Session, 0, len(m.sessions))
	for _, item := range m.sessions {
		items = append(items, item)
	}
	retry := make(map[string]struct{})
	for exitID := range m.cooldowns {
		if exitID != "" {
			retry[exitID] = struct{}{}
		}
	}
	m.sessions = make(map[uint64]*Session)
	m.pendingAnswers = make(map[uint64]protocol.P2PControlMessage)
	m.cooldowns = make(map[string]failureState)
	m.starting = make(map[string]uint64)
	m.mu.Unlock()

	for _, item := range items {
		item.mu.RLock()
		clientRole, exitID := item.clientRole, item.ExitDeviceID
		item.mu.RUnlock()
		if clientRole && exitID != "" {
			retry[exitID] = struct{}{}
		}
		m.closeAndNotify(item, "network_changed")
	}
	if m.send != nil && (m.local != nil || m.endpointFactory != nil) {
		for exitID := range retry {
			m.EnsureClient(exitID)
		}
	}
}

func (m *Manager) recordFailure(exitDeviceID, reason string) {
	if m == nil || exitDeviceID == "" {
		return
	}
	m.mu.Lock()
	state := m.cooldowns[exitDeviceID]
	state.count++
	switch state.count {
	case 1:
		state.until = time.Now().Add(5 * time.Second)
	case 2:
		state.until = time.Now().Add(30 * time.Second)
	case 3:
		state.until = time.Now().Add(2 * time.Minute)
	default:
		state.until = time.Now().Add(10 * time.Minute)
	}
	state.reason = reason
	m.cooldowns[exitDeviceID] = state
	m.mu.Unlock()
}

func (m *Manager) resetFailure(exitDeviceID string) {
	if m == nil || exitDeviceID == "" {
		return
	}
	m.mu.Lock()
	delete(m.cooldowns, exitDeviceID)
	m.mu.Unlock()
}

func (m *Manager) effectiveClientLimit() int {
	if m == nil {
		return 0
	}
	if m.powerConstrained.Load() && m.lowPowerMaxSessions > 0 {
		return m.lowPowerMaxSessions
	}
	return m.maxExitSessions
}

func (m *Manager) effectiveIdleTimeout() time.Duration {
	if m == nil {
		return 0
	}
	if m.powerConstrained.Load() && m.lowPowerIdleTimeout > 0 {
		return m.lowPowerIdleTimeout
	}
	return m.idleTimeout
}

func (m *Manager) directQUICOptions() directp2p.QUICOptions {
	if m == nil {
		return directp2p.QUICOptions{}
	}
	if m.powerConstrained.Load() {
		return directp2p.QUICOptions{
			MaxIdleTimeout:   m.effectiveIdleTimeout(),
			DisableKeepAlive: true,
		}
	}
	return directp2p.QUICOptions{KeepAlivePeriod: m.keepAlive, MaxIdleTimeout: m.idleTimeout}
}

func (m *Manager) enforceClientLimit(keepID uint64) {
	limit := m.effectiveClientLimit()
	if m == nil || limit <= 0 {
		return
	}
	for {
		m.mu.Lock()
		count := 0
		var oldest *Session
		var oldestUsed int64
		for _, item := range m.sessions {
			item.mu.RLock()
			clientRole, state := item.clientRole, item.state
			item.mu.RUnlock()
			if !clientRole || state != StateReady {
				continue
			}
			count++
			used := item.lastUsed.Load()
			if item.ID == keepID {
				continue
			}
			if oldest == nil || used < oldestUsed {
				oldest, oldestUsed = item, used
			}
		}
		if count <= limit || oldest == nil {
			m.mu.Unlock()
			return
		}
		delete(m.sessions, oldest.ID)
		m.mu.Unlock()
		m.closeAndNotify(oldest, "lru_evicted")
	}
}

func (m *Manager) reapIdleLoop() {
	interval := 10 * time.Second
	shortestIdle := m.idleTimeout
	if m.lowPowerIdleTimeout > 0 && (shortestIdle <= 0 || m.lowPowerIdleTimeout < shortestIdle) {
		shortestIdle = m.lowPowerIdleTimeout
	}
	if shortestIdle > 0 && shortestIdle/4 < interval {
		interval = shortestIdle / 4
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			var stale []*Session
			idleTimeout := m.effectiveIdleTimeout()
			powerConstrained := m.powerConstrained.Load()
			m.mu.Lock()
			for id, item := range m.sessions {
				item.mu.RLock()
				clientRole, state, direct := item.clientRole, item.state, item.direct
				item.mu.RUnlock()
				if state != StateReady || idleTimeout <= 0 {
					continue
				}
				// Desktop Exit sessions are retained normally. In constrained mode,
				// both roles may discard an idle path to eliminate NAT keepalive cost.
				if !clientRole && !powerConstrained {
					continue
				}
				if direct != nil && direct.ActiveStreams() > 0 {
					continue
				}
				last := time.UnixMilli(item.lastUsed.Load())
				if now.Sub(last) >= idleTimeout {
					delete(m.sessions, id)
					stale = append(stale, item)
				}
			}
			m.mu.Unlock()
			for _, item := range stale {
				m.closeAndNotify(item, "idle_timeout")
			}
		}
	}
}

func (m *Manager) closeAndNotify(item *Session, reason string) {
	if item == nil {
		return
	}
	message := protocol.P2PControlMessage{
		Type: protocol.P2PControlClose, SessionID: item.ID,
		ClientDeviceID: item.ClientDeviceID, ExitDeviceID: item.ExitDeviceID,
		SessionToken: append([]byte(nil), item.Token...), Reason: reason,
	}
	item.closeLocal()
	if m.send == nil || m.closed.Load() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
		defer cancel()
		_, _ = m.send(ctx, message)
	}()
}

func (m *Manager) removeFailedSession(item *Session, reason string) {
	if m == nil || item == nil {
		return
	}
	m.mu.Lock()
	current := m.sessions[item.ID]
	if current == item {
		delete(m.sessions, item.ID)
	}
	m.mu.Unlock()
	if current == item {
		m.closeAndNotify(item, reason)
	}
}
