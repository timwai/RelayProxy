package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"
)

type Mode string

const (
	ModeAuto     Mode = "auto"
	ModeQUICOnly Mode = "quic_only"
	ModeTCPOnly  Mode = "tcp_only"
)

type State string

const (
	StateDisconnected State = "DISCONNECTED"
	StateConnecting   State = "CONNECTING"
	StateConnected    State = "CONNECTED"
	StateReconnecting State = "RECONNECTING"
	StateClosed       State = "CLOSED"
)

type ManagerConfig struct {
	ServerAddress  string        // e.g. "1.2.3.4" or "relay.example.com"
	QUICPort       int           // default 443
	TCPPort        int           // default 443
	Mode           Mode          // auto, quic_only, tcp_only
	TLSConfig      *tls.Config   // TLS configuration
	PlainTCP       bool          // Explicitly disable TLS; a nil TLSConfig alone uses secure defaults.
	ConnectTimeout time.Duration // default 10s
}

type TunnelManager struct {
	cfg         ManagerConfig
	mu          sync.RWMutex
	connectMu   sync.Mutex
	stateEvents sync.Mutex
	state       State
	session     TunnelSession
	ctx         context.Context
	cancel      context.CancelFunc
	onState     func(oldState, newState State, session TunnelSession)
	notifyChan  chan struct{}
	startOnce   sync.Once
	wg          sync.WaitGroup
}

func NewTunnelManager(cfg ManagerConfig, onState func(oldState, newState State, session TunnelSession)) *TunnelManager {
	if cfg.QUICPort == 0 {
		cfg.QUICPort = 443
	}
	if cfg.TCPPort == 0 {
		cfg.TCPPort = 443
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeAuto
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 10 * time.Second
	}
	if cfg.PlainTCP {
		cfg.TLSConfig = nil
	} else {
		if cfg.TLSConfig == nil {
			cfg.TLSConfig = &tls.Config{}
		}
		cfg.TLSConfig = cfg.TLSConfig.Clone()
		if cfg.TLSConfig.MinVersion < tls.VersionTLS13 {
			cfg.TLSConfig.MinVersion = tls.VersionTLS13
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &TunnelManager{
		cfg:        cfg,
		state:      StateDisconnected,
		ctx:        ctx,
		cancel:     cancel,
		onState:    onState,
		notifyChan: make(chan struct{}, 1),
	}
}

func (m *TunnelManager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *TunnelManager) Session() TunnelSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.session
}

func (m *TunnelManager) setState(newState State, session TunnelSession) bool {
	m.stateEvents.Lock()
	defer m.stateEvents.Unlock()
	m.mu.Lock()
	if m.state == StateClosed {
		m.mu.Unlock()
		if session != nil {
			_ = session.Close()
		}
		return false
	}
	oldState := m.state
	m.state = newState
	m.session = session
	m.mu.Unlock()

	if m.onState != nil && oldState != newState {
		m.onState(oldState, newState, session)
	}

	if newState == StateDisconnected {
		select {
		case m.notifyChan <- struct{}{}:
		default:
		}
	}
	return true
}

// MarkSessionLost clears the active session if it matches sess and triggers reconnect.
func (m *TunnelManager) MarkSessionLost(sess TunnelSession) {
	if sess == nil {
		return
	}
	m.stateEvents.Lock()
	defer m.stateEvents.Unlock()
	m.mu.Lock()
	if m.session != sess {
		m.mu.Unlock()
		return
	}
	oldState := m.state
	m.session = nil
	m.state = StateDisconnected
	m.mu.Unlock()
	_ = sess.Close()
	if m.onState != nil && oldState != StateDisconnected {
		m.onState(oldState, StateDisconnected, nil)
	}
	select {
	case m.notifyChan <- struct{}{}:
	default:
	}
}

// Connect dials the relay server according to the configured mode
func (m *TunnelManager) Connect(ctx context.Context) (TunnelSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	m.connectMu.Lock()
	defer m.connectMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.State() == StateClosed {
		return nil, net.ErrClosed
	}
	if m.cfg.PlainTCP && m.cfg.Mode == ModeQUICOnly {
		return nil, errors.New("quic_only requires TLS; plaintext TCP must use auto or tcp_only")
	}

	if sess := m.Session(); sess != nil {
		select {
		case <-sess.Done():
			m.MarkSessionLost(sess)
		default:
			return sess, nil
		}
	}

	m.setState(StateConnecting, nil)

	quicAddr := net.JoinHostPort(m.cfg.ServerAddress, strconv.Itoa(m.cfg.QUICPort))
	tcpAddr := net.JoinHostPort(m.cfg.ServerAddress, strconv.Itoa(m.cfg.TCPPort))

	var session TunnelSession
	var err error
	var quicErr error

	// 1. Try QUIC if ModeAuto or ModeQUICOnly
	if m.cfg.TLSConfig != nil && (m.cfg.Mode == ModeAuto || m.cfg.Mode == ModeQUICOnly) {
		budget := m.cfg.ConnectTimeout
		if m.cfg.Mode == ModeAuto {
			budget = min(budget/2, 4*time.Second)
			if deadline, ok := ctx.Deadline(); ok {
				budget = min(budget, time.Until(deadline)/2)
			}
		}
		dialCtx, dialCancel := context.WithTimeout(ctx, budget)
		quicCfg := DefaultQUICConfig()
		session, err = DialQUIC(dialCtx, quicAddr, m.cfg.TLSConfig, quicCfg)
		dialCancel()

		if err == nil {
			if ctx.Err() != nil {
				_ = session.Close()
				return nil, ctx.Err()
			}
			if !m.setState(StateConnected, session) {
				return nil, net.ErrClosed
			}
			return session, nil
		}
		log.Printf("[TunnelManager] QUIC connection to %s failed: %v", quicAddr, err)
		quicErr = fmt.Errorf("QUIC: %w", err)

		if m.cfg.Mode == ModeQUICOnly {
			m.setState(StateDisconnected, nil)
			return nil, fmt.Errorf("quic connection failed: %w", err)
		}
	}

	// 2. Fallback to TLS + yamux if ModeAuto or ModeTCPOnly
	if m.cfg.Mode == ModeAuto || m.cfg.Mode == ModeTCPOnly {
		dialCtx, dialCancel := context.WithTimeout(ctx, m.cfg.ConnectTimeout)
		session, err = DialTLS(dialCtx, tcpAddr, m.cfg.TLSConfig, nil)
		dialCancel()

		if err == nil {
			if ctx.Err() != nil {
				_ = session.Close()
				return nil, ctx.Err()
			}
			if !m.setState(StateConnected, session) {
				return nil, net.ErrClosed
			}
			return session, nil
		}
		log.Printf("[TunnelManager] TLS connection to %s failed: %v", tcpAddr, err)
	}

	m.setState(StateDisconnected, nil)
	return nil, fmt.Errorf("all transport connection attempts failed: %w", errors.Join(quicErr, err))
}

// StartAutoReconnect runs the background reconnect loop
func (m *TunnelManager) StartAutoReconnect() {
	m.startOnce.Do(func() {
		m.mu.Lock()
		if m.state == StateClosed {
			m.mu.Unlock()
			return
		}
		m.wg.Add(1)
		m.mu.Unlock()
		go func() { defer m.wg.Done(); m.reconnectLoop() }()
	})
}

func (m *TunnelManager) reconnectLoop() {
	backoffs := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
	}
	backoffIdx := 0

	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		sess := m.Session()
		if sess != nil {
			// Connected: wait for session death or explicit disconnect.
			// Control-plane heartbeat already covers e2e liveness; do not OpenStream-probe
			// (that polluted relay logs and could falsely tear down the tunnel).
			select {
			case <-m.ctx.Done():
				return
			case <-sess.Done():
				log.Printf("[TunnelManager] Session died, triggering reconnect")
				m.MarkSessionLost(sess)
			case <-m.notifyChan:
				// Explicit disconnect notification
			}
			continue
		}

		m.setState(StateReconnecting, nil)

		// Calculate backoff with +/- 20% jitter
		base := backoffs[backoffIdx]
		jitter := time.Duration(float64(base) * (0.8 + 0.4*rand.Float64()))

		select {
		case <-m.ctx.Done():
			return
		case <-time.After(jitter):
		}

		// Allow sufficient timeout budget for all dial attempts
		ctx, cancel := context.WithTimeout(m.ctx, m.cfg.ConnectTimeout*2)
		_, err := m.Connect(ctx)
		cancel()

		if err == nil {
			backoffIdx = 0 // Reset backoff on success
		} else {
			if backoffIdx < len(backoffs)-1 {
				backoffIdx++
			}
		}
	}
}

// OpenStream opens a stream on the active session
func (m *TunnelManager) OpenStream(ctx context.Context) (TunnelStream, error) {
	m.mu.RLock()
	sess := m.session
	m.mu.RUnlock()

	if sess == nil {
		return nil, errors.New("tunnel is not connected")
	}

	return sess.OpenStream(ctx)
}

func (m *TunnelManager) Close() error {
	m.cancel()
	m.mu.Lock()
	m.state = StateClosed
	sess := m.session
	m.session = nil
	m.mu.Unlock()
	var err error
	if sess != nil {
		err = sess.Close()
	}
	m.connectMu.Lock()
	m.connectMu.Unlock()
	m.wg.Wait()
	return err
}
