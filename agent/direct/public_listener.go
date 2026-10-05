package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"

	"relayproxy/agent/exit"
	"relayproxy/internal/acl"
	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

const PublicQUICALPN = "relayproxy-public-direct-v1"

type SessionAuthorizer func(context.Context, tunnel.TunnelSession) (*acl.Policy, error)

type PublicListenerConfig struct {
	ListenAddress string
	TLSConfig     *tls.Config
	QUICOptions   directtransport.QUICOptions
	Authorize     SessionAuthorizer
	MaxStreams    int
}

// PublicListener owns the long-lived Exit listener for Public Direct. It is not
// wired into production startup until endpoint verification and ticket auth are
// available; authorization is mandatory even for local integration use.
type PublicListener struct {
	listener   *directtransport.Listener
	handler    *exit.Handler
	authorize  SessionAuthorizer
	maxStreams int
	wg         sync.WaitGroup
}

func ListenPublic(config PublicListenerConfig, handler *exit.Handler) (*PublicListener, error) {
	if handler == nil {
		return nil, errors.New("public direct exit handler is required")
	}
	if config.Authorize == nil {
		return nil, errors.New("public direct authorizer is required")
	}
	tlsConfig, err := publicTLSConfig(config.TLSConfig)
	if err != nil {
		return nil, err
	}
	if config.ListenAddress == "" {
		config.ListenAddress = ":0"
	}
	listener, err := directtransport.ListenAddr(config.ListenAddress, tlsConfig, config.QUICOptions)
	if err != nil {
		return nil, err
	}
	maxStreams := config.MaxStreams
	if maxStreams <= 0 {
		maxStreams = 1024
	}
	return &PublicListener{
		listener: listener, handler: handler, authorize: config.Authorize, maxStreams: maxStreams,
	}, nil
}

func publicTLSConfig(config *tls.Config) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("public direct TLS config is required")
	}
	config = config.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	config.NextProtos = []string{PublicQUICALPN}
	return config, nil
}

func (l *PublicListener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *PublicListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	err := l.listener.Close()
	l.wg.Wait()
	return err
}

func (l *PublicListener) Serve(ctx context.Context) error {
	if l == nil || l.listener == nil {
		return net.ErrClosed
	}
	for {
		session, err := l.listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		l.wg.Add(1)
		go func(session tunnel.TunnelSession) {
			defer l.wg.Done()
			defer session.Close()
			policy, err := l.authorize(ctx, session)
			if err != nil || policy == nil {
				return
			}
			l.serveAuthorizedSession(ctx, session, policy)
		}(session)
	}
}

func (l *PublicListener) serveAuthorizedSession(ctx context.Context, session tunnel.TunnelSession, policy *acl.Policy) {
	boundCtx := exit.BindRelayPolicy(ctx, policy)
	sem := make(chan struct{}, l.maxStreams)
	var workers sync.WaitGroup
	defer workers.Wait()

	for {
		stream, err := session.AcceptStream(ctx)
		if err != nil {
			return
		}
		select {
		case sem <- struct{}{}:
		default:
			_ = stream.Close()
			continue
		}
		_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
		header, err := protocol.ReadStreamHeader(stream)
		if err != nil || !publicProxyFrame(header.Type) {
			<-sem
			_ = stream.Close()
			continue
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-sem }()
			l.handler.HandleStreamWithHeader(boundCtx, stream, header)
		}()
	}
}

func publicProxyFrame(frameType uint8) bool {
	switch frameType {
	case protocol.FrameTypeOpenTCP, protocol.FrameTypeOpenUDP, protocol.FrameTypeSpeedTest:
		return true
	default:
		return false
	}
}
