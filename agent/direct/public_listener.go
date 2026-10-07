package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/apernet/quic-go"
	"relayproxy/internal/acl"
	quiccongestion "relayproxy/internal/congestion"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const (
	defaultAuthTimeout             = 5 * time.Second
	defaultMaxConcurrentHandshakes = 64
)

type ListenerConfig struct {
	ListenAddress           string
	TLSConfig               *tls.Config
	QUICConfig              *quic.Config
	Authenticator           Authenticator
	AuthTimeout             time.Duration
	AuthAttemptsPerMinute   int
	MaxConcurrentHandshakes int
	DisableLossCompensation bool
}

type AcceptedSession struct {
	Tunnel         tunnel.TunnelSession
	ClientDeviceID string
	ExitDeviceID   string
	RelayPolicy    *acl.Policy
}

type acceptResult struct {
	accepted *AcceptedSession
	err      error
}

type PublicListener struct {
	transport     *tunnel.DirectQUICListener
	authenticator Authenticator
	authTimeout   time.Duration
	limiter       *handshakeLimiter
	disableLossCompensation bool

	ctx         context.Context
	cancel      context.CancelFunc
	results     chan acceptResult
	slots       chan struct{}
	acceptWG    sync.WaitGroup
	handshakeWG sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

func Listen(config ListenerConfig) (*PublicListener, error) {
	if strings.TrimSpace(config.ListenAddress) == "" {
		return nil, errors.New("public direct listen address is required")
	}
	if config.Authenticator == nil {
		return nil, ErrAuthenticatorRequired
	}
	timeout := config.AuthTimeout
	if timeout <= 0 {
		timeout = defaultAuthTimeout
	}
	transport, err := tunnel.ListenDirectQUIC(config.ListenAddress, config.TLSConfig, config.QUICConfig)
	if err != nil {
		return nil, err
	}
	maxHandshakes := config.MaxConcurrentHandshakes
	if maxHandshakes <= 0 {
		maxHandshakes = defaultMaxConcurrentHandshakes
	}
	listenerCtx, cancel := context.WithCancel(context.Background())
	listener := &PublicListener{
		transport: transport, authenticator: config.Authenticator, authTimeout: timeout,
		limiter: newHandshakeLimiter(config.AuthAttemptsPerMinute),
		disableLossCompensation: config.DisableLossCompensation,
		ctx:     listenerCtx, cancel: cancel,
		results: make(chan acceptResult, maxHandshakes),
		slots:   make(chan struct{}, maxHandshakes),
	}
	listener.acceptWG.Add(1)
	go listener.acceptLoop()
	return listener, nil
}

func (l *PublicListener) Addr() string {
	if l == nil || l.transport == nil || l.transport.Addr() == nil {
		return ""
	}
	return l.transport.Addr().String()
}

func (l *PublicListener) Close() error {
	if l == nil || l.transport == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
		l.closeErr = l.transport.Close()
		l.acceptWG.Wait()
		l.handshakeWG.Wait()
		close(l.results)
		for result := range l.results {
			if result.accepted != nil && result.accepted.Tunnel != nil {
				_ = result.accepted.Tunnel.Close()
			}
		}
	})
	return l.closeErr
}

func (l *PublicListener) Accept(ctx context.Context) (*AcceptedSession, error) {
	if l == nil || l.transport == nil || l.ctx == nil {
		return nil, net.ErrClosed
	}
	select {
	case result, ok := <-l.results:
		if !ok {
			return nil, net.ErrClosed
		}
		return result.accepted, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (l *PublicListener) acceptLoop() {
	defer l.acceptWG.Done()
	for {
		session, err := l.transport.Accept(l.ctx)
		if err != nil {
			if l.ctx.Err() == nil {
				l.deliver(acceptResult{err: err})
			}
			return
		}
		select {
		case l.slots <- struct{}{}:
			l.handshakeWG.Add(1)
			go l.handleHandshake(session)
		default:
			// Fail closed when the bounded authentication pool is saturated.
			_ = session.Close()
		}
	}
}

func (l *PublicListener) handleHandshake(session tunnel.TunnelSession) {
	defer l.handshakeWG.Done()
	defer func() { <-l.slots }()

	accepted, probe, err := l.handshake(l.ctx, session)
	if err != nil {
		_ = session.Close()
		l.deliver(acceptResult{err: err})
		return
	}
	if probe {
		// The verifier owns connection shutdown after it has consumed the
		// challenge response. Sending an application close from this side can
		// overtake stream delivery and turn a successful probe into session closed.
		return
	}
	if accepted == nil || accepted.Tunnel == nil {
		_ = session.Close()
		return
	}
	if !l.deliver(acceptResult{accepted: accepted}) {
		_ = accepted.Tunnel.Close()
	}
}

func (l *PublicListener) deliver(result acceptResult) bool {
	select {
	case l.results <- result:
		return true
	case <-l.ctx.Done():
		return false
	}
}

func (l *PublicListener) handshake(ctx context.Context, session tunnel.TunnelSession) (*AcceptedSession, bool, error) {
	authCtx, cancel := context.WithTimeout(ctx, l.authTimeout)
	defer cancel()

	stream, err := session.AcceptStream(authCtx)
	if err != nil {
		return nil, false, fmt.Errorf("%w: authentication stream unavailable", ErrUnauthorized)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(l.authTimeout))
	if l.limiter == nil || !l.limiter.Allow(session.RemoteAddr(), time.Now()) {
		_ = writeHandshakeFailure(stream, protocol.ErrCodeRateLimited, "public direct authentication rate limited")
		return nil, false, fmt.Errorf("%w: authentication rate limited", ErrUnauthorized)
	}

	var handshake protocol.PublicDirectHandshakeRequest
	if err := protocol.ReadJSON(stream, &handshake); err != nil {
		_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid handshake request")
		return nil, false, fmt.Errorf("%w: invalid handshake request", ErrUnauthorized)
	}
	switch handshake.Type {
	case protocol.PublicDirectHandshakeProbe:
		if handshake.Probe == nil || len(handshake.Probe.Nonce) < 16 || len(handshake.Probe.Nonce) > 64 {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid probe request")
			return nil, false, fmt.Errorf("%w: invalid probe request", ErrUnauthorized)
		}
		if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{
			Success: true,
			Probe: &protocol.PublicDirectProbeResponse{
				Nonce: append([]byte(nil), handshake.Probe.Nonce...),
			},
		}); err != nil {
			return nil, false, fmt.Errorf("public direct probe response: %w", err)
		}
		// Do not close the whole QUIC connection until the verifier has read the
		// response. Closing the connection immediately can race buffered stream
		// delivery and surface a spurious "session closed" on the Server.
		_ = stream.CloseWrite()
		_ = stream.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		var drain [1]byte
		_, _ = stream.Read(drain[:])
		return nil, true, nil

	case protocol.PublicDirectHandshakeAuth:
		if handshake.Auth == nil {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
			return nil, false, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
		}
		request := *handshake.Auth
		request.ClientDeviceID = strings.TrimSpace(request.ClientDeviceID)
		request.ExitDeviceID = strings.TrimSpace(request.ExitDeviceID)
		if request.Version != protocol.PublicDirectAuthVersion ||
			request.ClientDeviceID == "" || request.ExitDeviceID == "" || len(request.Ticket) == 0 {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "invalid authentication request")
			return nil, false, fmt.Errorf("%w: invalid authentication request", ErrUnauthorized)
		}
		var relayPolicy *acl.Policy
		if authenticator, ok := l.authenticator.(PolicyAuthenticator); ok {
			policy, err := authenticator.AuthenticatePolicy(authCtx, request)
			if err != nil {
				_ = writeHandshakeFailure(stream, protocol.ErrCodeAuthFailed, "authentication failed")
				return nil, false, fmt.Errorf("%w: ticket rejected", ErrUnauthorized)
			}
			relayPolicy = policy
		} else if err := l.authenticator.Authenticate(authCtx, request); err != nil {
			_ = writeHandshakeFailure(stream, protocol.ErrCodeAuthFailed, "authentication failed")
			return nil, false, fmt.Errorf("%w: ticket rejected", ErrUnauthorized)
		}
		uploadBPS := quiccongestion.CapRequestedRate(request.BrutalUploadBPS, 0)
		downloadBPS := quiccongestion.CapRequestedRate(request.BrutalDownloadBPS, 0)
		if err := protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{
			Success: true, BrutalUploadBPS: uploadBPS, BrutalDownloadBPS: downloadBPS,
		}); err != nil {
			return nil, false, fmt.Errorf("public direct authentication response: %w", err)
		}
		_ = stream.SetDeadline(time.Time{})
		if downloadBPS > 0 {
			tunnel.UseBrutal(session, downloadBPS, l.disableLossCompensation)
		}
		return &AcceptedSession{
			Tunnel:         session,
			ClientDeviceID: request.ClientDeviceID,
			ExitDeviceID:   request.ExitDeviceID,
			RelayPolicy:    relayPolicy,
		}, false, nil
	default:
		_ = writeHandshakeFailure(stream, protocol.ErrCodeInvalidRequest, "unsupported handshake type")
		return nil, false, fmt.Errorf("%w: unsupported handshake type", ErrUnauthorized)
	}
}

func writeHandshakeFailure(stream tunnel.TunnelStream, code, message string) error {
	return protocol.WriteJSON(stream, protocol.PublicDirectHandshakeResponse{
		Success: false, ErrorCode: code, ErrorMessage: message,
	})
}
