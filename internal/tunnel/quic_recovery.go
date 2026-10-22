package tunnel

import (
	"context"
	"log"
	"net"
	"strconv"
	"time"
)

// waitConnected keeps TLS serving traffic while auto retries QUIC. A failed
// retry never changes the active session or invokes an application callback.
// There is at most one retry in flight, canceled and joined on session loss,
// network change or shutdown. QUIC and explicitly selected TCP stay untouched.
func (m *TunnelManager) waitConnected(sess TunnelSession) {
	ctx, cancel := context.WithCancel(m.ctx)
	results := make(chan transportDialResult, 1)
	pending := false
	defer func() {
		cancel()
		if pending {
			closeLateTransportResults(results, 1)
		}
	}()

	var retry <-chan time.Time
	var timer *time.Timer
	delay := m.cfg.AutoQUICRetryInterval
	if m.cfg.Mode == ModeAuto && !m.cfg.PlainTCP && sess.Transport() == TransportTLS {
		timer = time.NewTimer(delay)
		defer timer.Stop()
		retry = timer.C
	}
	var started time.Time
	var networkGeneration uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Done():
			log.Printf("[TunnelManager] Session died, triggering reconnect")
			m.MarkSessionLost(sess)
			return
		case <-m.notifyChan:
			return
		case <-retry:
			retry = nil
			started = time.Now()
			m.mu.Lock()
			networkGeneration = m.metrics.NetworkChanges
			m.metrics.QUICRecoveryAttempts++
			m.mu.Unlock()
			pending = true
			go func() {
				dialCtx, dialCancel := context.WithTimeout(ctx, m.cfg.ConnectTimeout)
				defer dialCancel()
				address := net.JoinHostPort(m.cfg.ServerAddress, strconv.Itoa(m.cfg.QUICPort))
				candidate, err := DialQUIC(dialCtx, address, m.cfg.TLSConfig, DefaultQUICConfig())
				// A typed nil *QUICSession must not escape as a non-nil interface.
				var session TunnelSession
				if candidate != nil {
					session = candidate
				}
				results <- transportDialResult{transport: TransportQUIC, session: session, err: err}
			}()
		case result := <-results:
			pending = false
			if result.err == nil {
				if !m.promoteQUIC(ctx, sess, result.session, time.Since(started), networkGeneration) {
					_ = result.session.Close()
				}
				return
			}
			if result.session != nil {
				_ = result.session.Close()
			}
			if ctx.Err() != nil {
				return
			}
			m.mu.Lock()
			m.metrics.QUICRecoveryError = result.err.Error()
			m.mu.Unlock()
			log.Printf("[TunnelManager] QUIC recovery failed, keeping TLS: %v", result.err)
			if delay < maxAutoQUICRetryInterval {
				delay = min(delay*2, maxAutoQUICRetryInterval)
			}
			timer.Reset(delay)
			retry = timer.C
		}
	}
}

// promoteQUIC changes transport only after the QUIC handshake succeeds. Emit
// the existing reconnecting/connected callbacks so desktop and Android revoke
// old approval state and authenticate the new session normally. This replaces
// a transport; it does not migrate streams from the old TLS session.
func (m *TunnelManager) promoteQUIC(ctx context.Context, previous, candidate TunnelSession, elapsed time.Duration, networkGeneration uint64) bool {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()
	m.stateEvents.Lock()
	defer m.stateEvents.Unlock()

	m.mu.Lock()
	if ctx.Err() != nil || m.state != StateConnected || m.session != previous || !m.networkAvailable || m.metrics.NetworkChanges != networkGeneration {
		m.mu.Unlock()
		return false
	}
	select {
	case <-previous.Done():
		m.mu.Unlock()
		return false
	case <-candidate.Done():
		m.mu.Unlock()
		return false
	default:
	}
	m.state, m.session = StateReconnecting, nil
	m.mu.Unlock()
	if m.onState != nil {
		m.onState(StateConnected, StateReconnecting, nil)
	}
	_ = previous.Close()

	m.mu.Lock()
	if ctx.Err() != nil || m.state == StateClosed || !m.networkAvailable || m.metrics.NetworkChanges != networkGeneration {
		m.mu.Unlock()
		return false
	}
	select {
	case <-candidate.Done():
		m.mu.Unlock()
		return false
	default:
	}
	m.state, m.session = StateConnected, candidate
	m.connectedAt = time.Now()
	m.metrics.QUICRecoveries++
	m.metrics.QUICRecoveryError = ""
	m.metrics.Transport = TransportQUIC
	m.metrics.FallbackReason = ""
	m.metrics.LastConnectMS = elapsed.Milliseconds()
	m.metrics.FirstByteMS, m.metrics.ControlReadyMS = 0, 0
	if diagnostics := DiagnoseSession(candidate); diagnostics != nil {
		m.metrics.Dial = diagnostics.Dial
	}
	m.mu.Unlock()
	log.Printf("[TunnelManager] QUIC recovered; replacing TLS and reauthenticating")
	if m.onState != nil {
		m.onState(StateReconnecting, StateConnected, candidate)
	}
	return true
}
