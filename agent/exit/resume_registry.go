package exit

import (
	"errors"
	"net"
	"sync"
	"time"

	p2presume "relayproxy/internal/p2p/resume"
)

var (
	errResumeSessionExists   = errors.New("resumable target session already exists")
	errResumeSessionNotFound = errors.New("resumable target session not found")
	errResumeSessionCapacity = errors.New("resumable target session capacity reached")
	errResumeSessionClosed   = errors.New("resumable target session is closed")
)

const (
	defaultResumeGrace       = 15 * time.Second
	defaultResumeMaxSessions = 256
)

type resumeRegistry struct {
	mu       sync.Mutex
	sessions map[[p2presume.StreamIDSize]byte]*resumeTargetSession
	grace    time.Duration
	max      int
}

type resumeTargetSession struct {
	registry *resumeRegistry
	identity p2presume.Identity
	target   net.Conn

	mu         sync.Mutex
	generation uint64
	local      p2presume.Binding
	detachedAt time.Time
	timer      *time.Timer
	closed     bool
}

func newResumeRegistry(grace time.Duration, maxSessions int) *resumeRegistry {
	if grace <= 0 {
		grace = defaultResumeGrace
	}
	if maxSessions <= 0 {
		maxSessions = defaultResumeMaxSessions
	}
	return &resumeRegistry{
		sessions: make(map[[p2presume.StreamIDSize]byte]*resumeTargetSession),
		grace:    grace,
		max:      maxSessions,
	}
}

// register transfers target ownership to the registry. The caller must close
// target if this method returns an error.
func (r *resumeRegistry) register(local p2presume.Binding, target net.Conn) (*resumeTargetSession, error) {
	if r == nil || target == nil || local.Type != p2presume.BindAck ||
		!local.Identity.Valid() || local.Generation == 0 {
		return nil, p2presume.ErrBinding
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[local.Identity.ID]; exists {
		return nil, errResumeSessionExists
	}
	if len(r.sessions) >= r.max {
		return nil, errResumeSessionCapacity
	}
	s := &resumeTargetSession{
		registry:   r,
		identity:   local.Identity,
		target:     target,
		generation: local.Generation,
		local:      local,
	}
	r.sessions[local.Identity.ID] = s
	return s, nil
}

func (r *resumeRegistry) get(id [p2presume.StreamIDSize]byte) (*resumeTargetSession, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	s, ok := r.sessions[id]
	r.mu.Unlock()
	return s, ok
}

// rebind authenticates a peer's replacement transport and advances the
// generation. The current local offsets are retained until detach publishes a
// newer local Binding. This keeps peer claims separate from Exit-owned state.
func (r *resumeRegistry) rebind(peer p2presume.Binding) (*resumeTargetSession, error) {
	if r == nil || peer.Type != p2presume.BindOpen || !peer.Identity.Valid() || peer.Generation == 0 {
		return nil, p2presume.ErrBinding
	}
	s, ok := r.get(peer.Identity.ID)
	if !ok {
		return nil, errResumeSessionNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errResumeSessionClosed
	}
	current := s.local
	current.Generation = s.generation
	if err := p2presume.ValidateRebind(current, peer); err != nil {
		return nil, err
	}
	s.generation = peer.Generation
	s.detachedAt = time.Time{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return s, nil
}

// detach publishes the Exit-side byte offsets for the just-lost transport and
// keeps the target socket alive for the configured recovery grace period.
func (s *resumeTargetSession) detach(local p2presume.Binding) error {
	if s == nil || local.Type != p2presume.BindAck || !local.Identity.Valid() {
		return p2presume.ErrBinding
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errResumeSessionClosed
	}
	if local.Identity.ID != s.identity.ID || !s.identity.VerifyToken(local.Identity.Token[:]) ||
		local.Generation != s.generation {
		s.mu.Unlock()
		return p2presume.ErrBinding
	}
	if local.SendOffset < s.local.SendOffset || local.ReceiveOffset < s.local.ReceiveOffset {
		s.mu.Unlock()
		return p2presume.ErrBinding
	}
	s.local = local
	s.detachedAt = time.Now()
	if s.timer != nil {
		s.timer.Stop()
	}
	registry := s.registry
	id := s.identity.ID
	generation := s.generation
	grace := defaultResumeGrace
	if registry != nil && registry.grace > 0 {
		grace = registry.grace
	}
	s.timer = time.AfterFunc(grace, func() {
		if registry != nil {
			registry.expire(id, generation)
		}
	})
	s.mu.Unlock()
	return nil
}

func (r *resumeRegistry) expire(id [p2presume.StreamIDSize]byte, generation uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	s := r.sessions[id]
	if s == nil {
		r.mu.Unlock()
		return
	}

	s.mu.Lock()
	if s.closed || s.generation != generation || s.detachedAt.IsZero() {
		s.mu.Unlock()
		r.mu.Unlock()
		return
	}
	// Mark the session unusable first, but keep it registered until the target
	// socket is actually closed. This makes len()==0 a strict resource-release
	// boundary rather than racing asynchronous Close().
	s.closed = true
	target := s.target
	s.target = nil
	s.timer = nil
	s.mu.Unlock()
	r.mu.Unlock()

	if target != nil {
		_ = target.Close()
	}

	r.mu.Lock()
	if r.sessions[id] == s {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
}

func (r *resumeRegistry) remove(id [p2presume.StreamIDSize]byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	s := r.sessions[id]
	if s != nil {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	if s != nil {
		s.closeTarget()
	}
}

func (r *resumeRegistry) closeAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	items := make([]*resumeTargetSession, 0, len(r.sessions))
	for id, s := range r.sessions {
		delete(r.sessions, id)
		items = append(items, s)
	}
	r.mu.Unlock()
	for _, s := range items {
		s.closeTarget()
	}
}

func (r *resumeRegistry) len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

func (s *resumeTargetSession) closeTarget() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	target := s.target
	s.target = nil
	s.mu.Unlock()
	if target != nil {
		_ = target.Close()
	}
}

func (s *resumeTargetSession) targetConn() (net.Conn, error) {
	if s == nil {
		return nil, errResumeSessionNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.target == nil {
		return nil, errResumeSessionClosed
	}
	return s.target, nil
}

func (s *resumeTargetSession) currentGeneration() uint64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation
}
