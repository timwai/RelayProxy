package divert

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"relayproxy/internal/traffic"
	"slices"
	"strings"
	"sync"
	"time"
)

// Dialer must be the raw tunnel dialer. A routing dialer would evaluate policy
// a second time and could override a process rule or its selected exit.
type Dialer interface {
	DialTCP(context.Context, string, string, uint16) (net.Conn, error)
	DialUDP(context.Context, string, string, uint16) (net.PacketConn, error)
}

type Options struct {
	Config             Config
	Dialer             Dialer
	Guard              LoopGuard
	ListenHost         string // retained for configuration compatibility
	PolicyMu           *sync.RWMutex
	MaxTCPFlows        int
	MaxUDPAssociations int
	UDPIdleTimeout     time.Duration
	DialTimeout        time.Duration
	UDPWriteTimeout    time.Duration
	SharedPolicy       func(Flow) Decision
	Traffic            *traffic.Registry
	DefaultExitID      func() string
}

// Server owns classified flows. OS interception is separately gated by a
// side-effect-free capability preflight. Trusted platform adapters must retain
// ClassifiedFlow and implement DIRECT/reject and original-source reply injection.
type Server struct {
	opts   Options
	engine *Engine
	dialer Dialer
	guard  LoopGuard

	ctx         context.Context
	cancel      context.CancelFunc
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	closed      bool
	done        chan struct{}
	wg          sync.WaitGroup
	interceptor systemInterceptor

	tcpFlows    int
	connections map[net.Conn]struct{}
	udp         map[FlowKey]*udpAssociation
	sweeping    bool
}

func New(opts Options) (*Server, error) {
	if opts.Dialer == nil {
		return nil, errors.New("divert: dialer required")
	}
	if opts.MaxTCPFlows < 0 || opts.MaxUDPAssociations < 0 || opts.UDPIdleTimeout < 0 ||
		opts.DialTimeout < 0 || opts.UDPWriteTimeout < 0 {
		return nil, errors.New("divert: limits and timeouts must not be negative")
	}
	if opts.MaxTCPFlows == 0 {
		opts.MaxTCPFlows = 1024
	}
	if opts.MaxUDPAssociations == 0 {
		opts.MaxUDPAssociations = 1024
	}
	if opts.UDPIdleTimeout == 0 {
		opts.UDPIdleTimeout = 60 * time.Second
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.UDPWriteTimeout == 0 {
		opts.UDPWriteTimeout = 5 * time.Second
	}
	cfg := cloneConfig(opts.Config)
	self := filepath.Base(os.Args[0])
	cfg.ExcludeProcesses = appendUnique(cfg.ExcludeProcesses, self, "relayproxy", "relayproxy.exe")
	eng, err := NewEngine(cfg)
	if err != nil {
		return nil, err
	}
	opts.Guard.SelfNames = appendUnique(opts.Guard.SelfNames, self, "relayproxy", "relayproxy.exe")
	opts.Guard.RelayPorts = slices.Clone(opts.Guard.RelayPorts)
	opts.Guard.RelayIPs = slices.Clone(opts.Guard.RelayIPs)
	opts.Guard.LocalProxy = slices.Clone(opts.Guard.LocalProxy)
	opts.Guard.LocalIPs = slices.Clone(opts.Guard.LocalIPs)
	opts.Guard.SelfPID = uint32(os.Getpid())
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		opts: opts, engine: eng, dialer: opts.Dialer, guard: opts.Guard,
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
		connections: make(map[net.Conn]struct{}), udp: make(map[FlowKey]*udpAssociation),
	}, nil
}

func appendUnique(in []string, add ...string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(in)+len(add))
	for _, s := range append(slices.Clone(in), add...) {
		// Do not discard empty user exclusions: validation must reject them.
		key := strings.TrimSpace(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

func (s *Server) Engine() *Engine { return s.engine }

// ReloadRules does not acquire PolicyMu. Its owner may hold that lock while
// publishing several policy engines atomically. Existing flows retain decisions.
func (s *Server) ReloadRules(cfg Config) error {
	cfg = cloneConfig(cfg)
	cfg.ExcludeProcesses = appendUnique(cfg.ExcludeProcesses, filepath.Base(os.Args[0]), "relayproxy", "relayproxy.exe")
	if s.Running() {
		if err := validatePlatformRules(cfg, PlatformCapabilities()); err != nil {
			return err
		}
	}
	return s.engine.Reload(cfg)
}

func (s *Server) Start() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.interceptor != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := Preflight(s.engine.Config()); err != nil {
		return err
	}
	interceptor, err := startPlatformInterceptor(s)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.interceptor = interceptor
	s.mu.Unlock()
	return nil
}

func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.interceptor != nil && s.interceptor.Running()
}

func (s *Server) ListenAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.interceptor == nil {
		return ""
	}
	return s.interceptor.ListenAddr()
}

// UDP is intercepted as datagrams; it does not expose a local proxy socket.
func (s *Server) UDPListenAddr() string { return "" }

// ClassifyFlow is the sole policy decision point. UDP packets sharing a complete
// original five-tuple and process identity reuse the same immutable decision.
func (s *Server) ClassifyFlow(input Flow) (*ClassifiedFlow, error) {
	if s.opts.PolicyMu != nil {
		s.opts.PolicyMu.RLock()
		defer s.opts.PolicyMu.RUnlock()
	}
	flow, key, err := validateFlow(input)
	if err != nil {
		return nil, err
	}
	var expired []*udpAssociation
	defer func() {
		for _, association := range expired {
			association.close()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	now := time.Now()
	if flow.Protocol == ProtoUDP {
		if current := s.udp[key]; current != nil {
			if !current.expired(now) && current.route.flow.Process == flow.Process && current.route.flow.ProcessID == flow.ProcessID {
				current.touch(now)
				return current.route, nil
			}
			delete(s.udp, key)
			expired = append(expired, current)
		}
		if len(s.udp) >= s.opts.MaxUDPAssociations {
			expired = append(expired, s.pruneUDPLocked(now)...)
		}
		if len(s.udp) >= s.opts.MaxUDPAssociations {
			return nil, ErrFlowCapacity
		}
	}

	decision := Decision{Action: ActionDirect, Rule: "loop-guard"}
	guarded := s.guard.MustDirectFlow(flow)
	if !guarded {
		decision = s.engine.MatchWith(flow, s.opts.SharedPolicy)
	}
	if decision.Action == ActionProxy && decision.ExitID == "" && s.opts.DefaultExitID != nil {
		decision.ExitID = s.opts.DefaultExitID()
	}
	route := &ClassifiedFlow{owner: s, key: key, flow: flow, decision: decision}
	if !guarded {
		exitID := decision.ExitID
		if decision.Action != ActionProxy {
			exitID = ""
		}
		accounting := "stream"
		if decision.Action == ActionDirect {
			accounting = "packet"
		}
		route.traffic = s.opts.Traffic.Start(traffic.Metadata{ProcessID: flow.ProcessID, Process: flow.Process,
			Source: key.Source.String(), Host: flow.Host, DomainSource: flow.DomainSource, IP: flow.IP, Port: flow.Port,
			Protocol: string(flow.Protocol), Entry: "transparent", Action: string(decision.Action), Rule: decision.Rule,
			ExitID: exitID, Accounting: accounting})
		if decision.Action == ActionReject {
			route.traffic.Finish("rejected", nil)
		}
	}
	if flow.Protocol == ProtoUDP {
		ctx, cancel := context.WithCancel(s.ctx)
		association := &udpAssociation{
			server: s, route: route, ctx: ctx, cancel: cancel, ready: make(chan struct{}),
		}
		association.touch(now)
		route.udp = association
		s.udp[key] = association
		s.startUDPSweeperLocked()
	}
	return route, nil
}

// ForwardTCP consumes a previously classified PROXY flow and owns downstream.
// It never rematches policy or turns a DIRECT decision into a new intercepted dial.
func (s *Server) ForwardTCP(ctx context.Context, route *ClassifiedFlow, downstream net.Conn) error {
	if downstream == nil {
		return errors.New("divert: TCP connection required")
	}
	defer downstream.Close()
	if route == nil || route.owner != s || route.key.Protocol != ProtoTCP {
		return errors.New("divert: invalid TCP classification")
	}
	if route.decision.Action != ActionProxy {
		return ErrNotProxyFlow
	}
	if !route.used.CompareAndSwap(false, true) {
		return errors.New("divert: TCP classification already consumed")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	if s.tcpFlows >= s.opts.MaxTCPFlows {
		s.mu.Unlock()
		return ErrFlowCapacity
	}
	s.tcpFlows++
	s.connections[downstream] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, downstream)
		s.tcpFlows--
		s.mu.Unlock()
		s.wg.Done()
	}()

	flowCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	dialCtx, dialCancel := context.WithTimeout(flowCtx, s.opts.DialTimeout)
	upstream, err := s.dialer.DialTCP(dialCtx, route.decision.ExitID, route.flow.IP, route.flow.Port)
	dialCancel()
	if err != nil {
		route.traffic.Finish("failed", err)
		return err
	}
	if upstream == nil {
		err := errors.New("divert: TCP dialer returned a nil connection")
		route.traffic.Finish("failed", err)
		return err
	}
	upstream = traffic.WrapConn(upstream, route.traffic)
	defer upstream.Close()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	s.connections[upstream] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.connections, upstream)
		s.mu.Unlock()
	}()
	err = bidirectionalCopy(flowCtx, downstream, upstream)
	if err != nil {
		route.traffic.Finish("failed", err)
	}
	return err
}

// Close closes all flow sockets, cancels pending dials, and waits for owned
// readers, writers and sweepers even when Start never installed an interceptor.
func (s *Server) Close() error {
	s.lifecycleMu.Lock()
	s.mu.Lock()
	if s.closed {
		done := s.done
		s.mu.Unlock()
		s.lifecycleMu.Unlock()
		<-done
		return nil
	}
	s.closed = true
	connections := make([]net.Conn, 0, len(s.connections))
	for conn := range s.connections {
		connections = append(connections, conn)
	}
	associations := make([]*udpAssociation, 0, len(s.udp))
	for _, association := range s.udp {
		associations = append(associations, association)
	}
	clear(s.udp)
	interceptor := s.interceptor
	s.mu.Unlock()
	s.lifecycleMu.Unlock()

	s.cancel()
	for _, association := range associations {
		association.close()
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	if interceptor != nil {
		interceptor.Close()
	}
	s.wg.Wait()
	close(s.done)
	return nil
}

func bidirectionalCopy(ctx context.Context, a, b net.Conn) error {
	stop := context.AfterFunc(ctx, func() {
		_ = a.Close()
		_ = b.Close()
	})
	defer stop()
	results := make(chan error, 2)
	copyOne := func(dst, src net.Conn) {
		_, err := io.Copy(dst, src)
		if err == nil {
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				err = half.CloseWrite()
			} else {
				err = errors.New("divert: tunnel does not support TCP half-close")
			}
		}
		results <- err
	}
	go copyOne(a, b)
	go copyOne(b, a)
	first := <-results
	if first != nil {
		_ = a.Close()
		_ = b.Close()
	}
	second := <-results
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(first, second)
}
