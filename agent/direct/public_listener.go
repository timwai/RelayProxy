package direct

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

type StreamHandler interface {
	HandleStream(context.Context, tunnel.TunnelStream)
}

// PublicListener owns the stable Exit UDP listener used by Public Direct. It is
// intentionally not wired into Agent startup until ticket authentication is
// implemented in Phase 4.
type PublicListener struct {
	ctx          context.Context
	cancel       context.CancelFunc
	listener     *directtransport.Listener
	authenticate SessionAuthenticator
	handler      StreamHandler

	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func ListenPublicQUIC(
	parent context.Context,
	address string,
	tlsConfig *tls.Config,
	authenticate SessionAuthenticator,
	handler StreamHandler,
) (*PublicListener, error) {
	if authenticate == nil {
		return nil, errors.New("Public Direct listener authenticator is required")
	}
	if handler == nil {
		return nil, errors.New("Public Direct stream handler is required")
	}
	config, err := publicTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}
	listener, err := directtransport.ListenAddr(address, config, directtransport.QUICConfig())
	if err != nil {
		return nil, err
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	public := &PublicListener{
		ctx:          ctx,
		cancel:       cancel,
		listener:     listener,
		authenticate: authenticate,
		handler:      handler,
	}
	public.wg.Add(1)
	go public.acceptLoop()
	return public, nil
}

func (l *PublicListener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *PublicListener) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.cancel()
		if l.listener != nil {
			l.closeErr = l.listener.Close()
		}
		l.wg.Wait()
	})
	return l.closeErr
}

func (l *PublicListener) acceptLoop() {
	defer l.wg.Done()
	for {
		session, err := l.listener.Accept(l.ctx)
		if err != nil {
			if l.ctx.Err() != nil {
				return
			}
			continue
		}
		l.wg.Add(1)
		go l.serveSession(session)
	}
}

func (l *PublicListener) serveSession(session tunnel.TunnelSession) {
	defer l.wg.Done()
	defer session.Close()
	if err := l.authenticate(l.ctx, session); err != nil {
		return
	}
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	for {
		stream, err := session.AcceptStream(l.ctx)
		if err != nil {
			return
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			l.handler.HandleStream(l.ctx, stream)
		}()
	}
}
