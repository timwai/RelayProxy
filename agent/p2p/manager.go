// Package p2p implements proxy direct-path signaling on the Agent.
// It intentionally does not own the QUIC data path yet: Phase 3 injects the
// local UDP candidates and ephemeral TLS identity through LocalDescription.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"relayproxy/internal/protocol"
)

type ControlSender func(context.Context, protocol.P2PControlMessage) (protocol.P2PControlMessage, error)
type LocalDescription func() ([]protocol.P2PCandidate, string, error)

type State string

const (
	StatePending State = "PENDING"
	StateReady   State = "READY"
	StateClosed  State = "CLOSED"
)

type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	send   ControlSender
	local  LocalDescription
	lease  time.Duration

	mu       sync.Mutex
	sessions map[uint64]*Session
	closed   atomic.Bool
}

type Session struct {
	manager *Manager

	ID             uint64
	ClientDeviceID string
	ExitDeviceID   string
	Token          []byte

	ExpiresAt atomic.Int64

	mu              sync.RWMutex
	peerCandidates  []protocol.P2PCandidate
	peerFingerprint string
	state           State
	closed          chan struct{}
	closeOnce       sync.Once
}

type Snapshot struct {
	ID              uint64
	ClientDeviceID  string
	ExitDeviceID    string
	ExpiresAt       int64
	PeerCandidates  []protocol.P2PCandidate
	PeerFingerprint string
	State           State
}

func NewManager(parent context.Context, send ControlSender, local LocalDescription, lease time.Duration) *Manager {
	if parent == nil {
		parent = context.Background()
	}
	if lease < 15*time.Second {
		lease = 60 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	return &Manager{ctx: ctx, cancel: cancel, send: send, local: local, lease: lease, sessions: make(map[uint64]*Session)}
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
	m.mu.Unlock()
	for _, item := range items {
		item.closeLocal()
	}
	return nil
}

func (m *Manager) StartClient(ctx context.Context, exitDeviceID string) (*Session, error) {
	if m == nil || m.send == nil || m.local == nil {
		return nil, errors.New("proxy P2P manager is not configured")
	}
	if m.closed.Load() {
		return nil, net.ErrClosed
	}
	if exitDeviceID == "" {
		return nil, errors.New("exit device id is required")
	}
	candidates, fingerprint, err := m.local()
	if err != nil {
		return nil, err
	}
	if fingerprint == "" {
		return nil, errors.New("ephemeral certificate fingerprint is required")
	}
	response, err := m.send(ctx, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectRequest, ExitDeviceID: exitDeviceID,
		Candidates: append([]protocol.P2PCandidate(nil), candidates...), CertFingerprint: fingerprint,
	})
	if err != nil {
		return nil, err
	}
	if response.Type == protocol.P2PControlError {
		return nil, fmt.Errorf("P2P connect rejected: [%s] %s", response.ErrorCode, response.ErrorMessage)
	}
	if response.Type != protocol.P2PControlLeaseAck || response.SessionID == 0 || len(response.SessionToken) < 16 {
		return nil, errors.New("P2P coordinator returned an incomplete session")
	}
	item := m.newSession(response.SessionID, response.ClientDeviceID, response.ExitDeviceID, response.SessionToken, response.LeaseExpiresAt)
	if item == nil {
		return nil, net.ErrClosed
	}
	return item, nil
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
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil && item.matchesToken(message.SessionToken) {
			item.setPeer(message.Candidates, message.PeerFingerprint, StateReady)
			if message.LeaseExpiresAt > 0 {
				item.ExpiresAt.Store(message.LeaseExpiresAt)
			}
		}
	case protocol.P2PControlCandidateUpdate:
		m.mu.Lock()
		item := m.sessions[message.SessionID]
		m.mu.Unlock()
		if item != nil && item.matchesToken(message.SessionToken) {
			item.setPeer(message.Candidates, "", "")
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
	if m.local == nil || m.send == nil || m.closed.Load() {
		return
	}
	candidates, fingerprint, err := m.local()
	if err != nil || fingerprint == "" {
		return
	}
	item := m.newSession(message.SessionID, message.ClientDeviceID, message.ExitDeviceID, message.SessionToken, message.LeaseExpiresAt)
	if item == nil {
		return
	}
	item.setPeer(message.Candidates, message.PeerFingerprint, StatePending)
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Second)
	response, err := m.send(ctx, protocol.P2PControlMessage{
		Type: protocol.P2PControlConnectAnswer, SessionID: message.SessionID,
		ClientDeviceID: message.ClientDeviceID, ExitDeviceID: message.ExitDeviceID,
		SessionToken: append([]byte(nil), message.SessionToken...),
		Candidates: append([]protocol.P2PCandidate(nil), candidates...), CertFingerprint: fingerprint,
	})
	cancel()
	if err != nil || response.Type == protocol.P2PControlError {
		m.remove(message.SessionID)
		return
	}
	if response.Type == protocol.P2PControlLeaseAck && response.LeaseExpiresAt > 0 {
		item.ExpiresAt.Store(response.LeaseExpiresAt)
	}
	item.setState(StateReady)
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

func (m *Manager) newSession(id uint64, clientDeviceID, exitDeviceID string, token []byte, expires int64) *Session {
	if id == 0 || len(token) < 16 {
		return nil
	}
	if expires == 0 {
		expires = time.Now().Add(m.lease).UnixMilli()
	}
	item := &Session{
		manager: m, ID: id, ClientDeviceID: clientDeviceID, ExitDeviceID: exitDeviceID,
		Token: append([]byte(nil), token...), state: StatePending, closed: make(chan struct{}),
	}
	item.ExpiresAt.Store(expires)
	m.mu.Lock()
	if m.closed.Load() {
		m.mu.Unlock()
		return nil
	}
	if old := m.sessions[id]; old != nil {
		old.closeLocal()
	}
	m.sessions[id] = item
	m.mu.Unlock()
	go item.renewLoop()
	return item
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

func (s *Session) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		ID: s.ID, ClientDeviceID: s.ClientDeviceID, ExitDeviceID: s.ExitDeviceID,
		ExpiresAt: s.ExpiresAt.Load(), PeerCandidates: append([]protocol.P2PCandidate(nil), s.peerCandidates...),
		PeerFingerprint: s.peerFingerprint, State: s.state,
	}
}

func (s *Session) setPeer(candidates []protocol.P2PCandidate, fingerprint string, state State) {
	s.mu.Lock()
	s.peerCandidates = append([]protocol.P2PCandidate(nil), candidates...)
	if fingerprint != "" {
		s.peerFingerprint = fingerprint
	}
	if state != "" {
		s.state = state
	}
	s.mu.Unlock()
}

func (s *Session) setState(state State) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

func (s *Session) matchesToken(token []byte) bool {
	if s == nil || len(token) != len(s.Token) {
		return false
	}
	var diff byte
	for i := range token {
		diff |= token[i] ^ s.Token[i]
	}
	return diff == 0
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
		}
	}
}

func (s *Session) closeLocal() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.setState(StateClosed)
		close(s.closed)
	})
}
