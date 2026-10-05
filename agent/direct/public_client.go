package direct

import (
	"context"
	"crypto/tls"
	"errors"

	directtransport "relayproxy/internal/direct"
	"relayproxy/internal/protocol"
	"relayproxy/internal/tunnel"
)

func DialPublicQUIC(
	ctx context.Context,
	address string,
	tlsConfig *tls.Config,
	authenticate SessionAuthenticator,
) (tunnel.TunnelSession, error) {
	if authenticate == nil {
		return nil, errors.New("Public Direct client authenticator is required")
	}
	config, err := publicTLSConfig(tlsConfig)
	if err != nil {
		return nil, err
	}
	session, err := directtransport.DialAddr(ctx, address, config, directtransport.QUICConfig())
	if err != nil {
		return nil, err
	}
	if err := authenticate(ctx, session); err != nil {
		_ = session.Close()
		return nil, err
	}
	// Public Direct QUIC always enables native datagrams. Phase 4 can replace
	// this optimistic local capability with authenticated capability exchange.
	tunnel.SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

func publicTLSConfig(config *tls.Config) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("Public Direct TLS config is required")
	}
	clone := config.Clone()
	if clone.MinVersion < tls.VersionTLS13 {
		clone.MinVersion = tls.VersionTLS13
	}
	if len(clone.NextProtos) == 0 {
		clone.NextProtos = []string{protocol.PublicDirectALPN}
	}
	return clone, nil
}
