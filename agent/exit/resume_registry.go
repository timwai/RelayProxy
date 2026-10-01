package exit

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	p2presume "relayproxy/internal/p2p/resume"
	"relayproxy/internal/tunnel"
)

var (
	errResumeSessionExists   = errors.New("resumable target session already exists")
	errResumeSessionNotFound = errors.New("resumable target session not found")
	errResumeSessionCapacity = errors.New("resumable target session capacity reached")
	errResumeSessionClosed   = errors.New("resumable target session is closed")
)

const (
	defaultResumeGrace             = 15 * time.Second
	defaultResumeMaxSessions       = 256
	defaultResumeReplayLimit       = 2 << 20
	defaultResumeTargetIdleTimeout = 5 * time.Minute
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

	mu           sync.Mutex
	generation   uint64
	local        p2presume.Binding
	detachedAt   time.Time
	timer        *time.Timer
	closed       bool
	state        *p2presume.StreamState
	endpoint     *p2presume.Endpoint
	ctx          context.Context
	cancel       context.CancelFunc
	remoteTarget string
	onDone       func()

	startOnce  sync.Once
	finishOnce sync.Once
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

func (r *resumeRegistry) registerLogical(
	local p2presume.Binding,
	target net.Conn,
	remoteTarget string,
	replayLimit int,
	onDone func(),
) (*resumeTargetSession, error) {
	if local.SendOffset != 0 || local.ReceiveOffset != 0 {
		return nil, p2presume.ErrBinding
	}
	if replayLimit <= 0 {
		replayLimit = defaultResumeReplayLimit
	}
	state, err := p2presume.NewStreamState(local.Identity, replayLimit)
	if err != nil {
		return nil, err
	}
	endpoint, err := p2presume.NewEndpoint(state)
	if err != nil {
		return nil, err
	}
	s, err := r.register(local, target)
	if err != nil {
		_ = endpoint.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.state = state
	s.endpoint = endpoint
	s.ctx = ctx
	s.cancel = cancel
	s.remoteTarget = remoteTarget
	s.onDone = onDone
	s.mu.Unlock()
	s.startLogicalBridge()
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
// generation. When the current generation is already detached, the same
// generation may be retried idempotently: the prior rebind response may have
// been lost after the Exit committed it. The current local offsets are retained
// until detach publishes a newer local Binding.
func (r *resumeRegistry) rebind(peer p2presume.Binding) (*resumeTargetSession, bool, error) {
	if r == nil || peer.Type != p2presume.BindOpen || !peer.Identity.Valid() || peer.Generation == 0 {
		return nil, false, p2presume.ErrBinding
	}
	s, ok := r.get(peer.Identity.ID)
	if !ok {
		return nil, false, errResumeSessionNotFound
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, errResumeSessionClosed
	}
	current := s.local
	if s.state != nil {
		if latest, err := s.state.Binding(p2presume.BindAck, s.generation); err == nil {
			current = latest
		}
	}
	current.Generation = s.generation
	retry := peer.Generation == s.generation
	if retry {
		if s.detachedAt.IsZero() {
			return nil, false, p2presume.ErrBinding
		}
		if err := p2presume.ValidateRebindRetryRequest(current, peer); err != nil {
			return nil, false, err
		}
	} else {
		if err := p2presume.ValidateRebindRequest(current, peer); err != nil {
			return nil, false, err
		}
		s.generation = peer.Generation
		s.local = current
		s.local.Generation = peer.Generation
	}
	s.detachedAt = time.Time{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return s, retry, nil
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

func (s *resumeTargetSession) startLogicalBridge() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		go func() {
			s.mu.Lock()
			ctx, endpoint, target := s.ctx, s.endpoint, s.target
			s.mu.Unlock()
			if ctx == nil || endpoint == nil || target == nil {
				s.finish()
				return
			}
			tunnel.Pipe(ctx, endpoint, target, defaultResumeTargetIdleTimeout, nil)
			if s.registry != nil {
				s.registry.remove(s.identity.ID)
			} else {
				s.closeTarget()
			}
			s.finish()
		}()
	})
}

func (s *resumeTargetSession) bindTransport(transport tunnel.TunnelStream, generation uint64) (<-chan struct{}, error) {
	return s.bindTransportMode(transport, generation, false)
}

func (s *resumeTargetSession) retryTransport(transport tunnel.TunnelStream, generation uint64) (<-chan struct{}, error) {
	return s.bindTransportMode(transport, generation, true)
}

func (s *resumeTargetSession) bindTransportMode(transport tunnel.TunnelStream, generation uint64, retry bool) (<-chan struct{}, error) {
	if s == nil || transport == nil || generation == 0 {
		return nil, p2presume.ErrBinding
	}
	s.mu.Lock()
	if s.closed || s.endpoint == nil || s.state == nil || generation != s.generation {
		s.mu.Unlock()
		return nil, errResumeSessionClosed
	}
	endpoint, state, ctx := s.endpoint, s.state, s.ctx
	s.mu.Unlock()
	var err error
	if retry {
		err = endpoint.RetryBind(transport, generation)
	} else {
		err = endpoint.Bind(transport, generation)
	}
	if err != nil {
		return nil, err
	}
	done := endpoint.GenerationDone(generation)
	go s.detachWhenGenerationEnds(ctx, state, generation, done)
	return done, nil
}

func (s *resumeTargetSession) detachWhenGenerationEnds(
	ctx context.Context,
	state *p2presume.StreamState,
	generation uint64,
	done <-chan struct{},
) {
	if s == nil || state == nil || generation == 0 || done == nil {
		return
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return
		case <-done:
		}
	} else {
		<-done
	}
	local, err := state.Binding(p2presume.BindAck, generation)
	if err != nil {
		return
	}
	// detach validates that this is still the current generation. If a newer
	// transport replaced the old one intentionally, the stale generation is
	// ignored instead of starting a recovery timer.
	_ = s.detach(local)
}

func (s *resumeTargetSession) localBinding() (p2presume.Binding, error) {
	if s == nil {
		return p2presume.Binding{}, errResumeSessionNotFound
	}
	s.mu.Lock()
	if s.closed || s.state == nil {
		s.mu.Unlock()
		return p2presume.Binding{}, errResumeSessionClosed
	}
	state, generation := s.state, s.generation
	s.mu.Unlock()
	return state.Binding(p2presume.BindAck, generation)
}

func (s *resumeTargetSession) remoteAddress() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteTarget
}

func (s *resumeTargetSession) finish() {
	if s == nil {
		return
	}
	s.finishOnce.Do(func() {
		s.mu.Lock()
		onDone := s.onDone
		s.onDone = nil
		s.mu.Unlock()
		if onDone != nil {
			onDone()
		}
	})
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
	s.closed = true
	target, endpoint, cancel := s.target, s.endpoint, s.cancel
	s.target, s.endpoint, s.cancel, s.timer = nil, nil, nil, nil
	s.mu.Unlock()
	r.mu.Unlock()

	if endpoint != nil {
		_ = endpoint.Close()
	}
	if target != nil {
		_ = target.Close()
	}
	if cancel != nil {
		cancel()
	}

	r.mu.Lock()
	if r.sessions[id] == s {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	s.finish()
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
		s.finish()
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
		s.finish()
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
	target, endpoint, cancel := s.target, s.endpoint, s.cancel
	s.target, s.endpoint, s.cancel = nil, nil, nil
	s.mu.Unlock()
	if endpoint != nil {
		_ = endpoint.Close()
	}
	if target != nil {
		_ = target.Close()
	}
	if cancel != nil {
		cancel()
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
