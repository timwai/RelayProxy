package direct

import (
	"context"
	"crypto/tls"
	"errors"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/tunnel"
)

type SessionAuthenticator func(context.Context, tunnel.TunnelSession) error

type PublicClientConfig struct {
	TLSConfig    *tls.Config
	QUICOptions  directtransport.QUICOptions
	Authenticate SessionAuthenticator
}

func DialPublic(ctx context.Context, address string, config PublicClientConfig) (tunnel.TunnelSession, error) {
	if config.Authenticate == nil {
		return nil, errors.New("public direct client authenticator is required")
	}
	tlsConfig, err := publicTLSConfig(config.TLSConfig)
	if err != nil {
		return nil, err
	}
	session, err := directtransport.DialAddr(ctx, address, tlsConfig, config.QUICOptions)
	if err != nil {
		return nil, err
	}
	if err := config.Authenticate(ctx, session); err != nil {
		_ = session.Close()
		return nil, err
	}
	return session, nil
}
