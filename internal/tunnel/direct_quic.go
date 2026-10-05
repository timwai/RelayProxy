package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"

	"github.com/quic-go/quic-go"
	"relayproxy/internal/protocol"
)

const DirectQUICALPN = "relayproxy-direct-v1"

// DirectQUICConfig returns the generic QUIC profile shared by direct transports.
// Authentication is deliberately owned by the caller; this helper only defines
// transport behavior.
func DirectQUICConfig(config *quic.Config) *quic.Config {
	if config == nil {
		config = DefaultQUICConfig()
	} else {
		config = config.Clone()
	}
	config.EnableDatagrams = true
	return config
}

func directTLSConfig(config *tls.Config, server bool) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("direct QUIC requires an explicit TLS configuration")
	}
	config = config.Clone()
	if config.MinVersion < tls.VersionTLS13 {
		config.MinVersion = tls.VersionTLS13
	}
	if !slices.Contains(config.NextProtos, DirectQUICALPN) {
		config.NextProtos = append(config.NextProtos, DirectQUICALPN)
	}
	if server && len(config.Certificates) == 0 && config.GetCertificate == nil {
		return nil, errors.New("direct QUIC listener requires a TLS certificate")
	}
	return config, nil
}

// DirectQUICListener is a long-lived QUIC listener suitable for transports
// whose UDP endpoint is stable across many client sessions.
type DirectQUICListener struct {
	listener *quic.Listener
}

func ListenDirectQUIC(address string, tlsConfig *tls.Config, quicConfig *quic.Config) (*DirectQUICListener, error) {
	tlsConfig, err := directTLSConfig(tlsConfig, true)
	if err != nil {
		return nil, err
	}
	listener, err := quic.ListenAddr(address, tlsConfig, DirectQUICConfig(quicConfig))
	if err != nil {
		return nil, fmt.Errorf("direct QUIC listen failed: %w", err)
	}
	return &DirectQUICListener{listener: listener}, nil
}

func (l *DirectQUICListener) Accept(ctx context.Context) (*QUICSession, error) {
	if l == nil || l.listener == nil {
		return nil, net.ErrClosed
	}
	conn, err := l.listener.Accept(ctx)
	if err != nil {
		return nil, err
	}
	session := NewQUICSession(conn)
	SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}

func (l *DirectQUICListener) Addr() net.Addr {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Addr()
}

func (l *DirectQUICListener) Close() error {
	if l == nil || l.listener == nil {
		return nil
	}
	return l.listener.Close()
}

func DialDirectQUIC(ctx context.Context, address string, tlsConfig *tls.Config, quicConfig *quic.Config) (*QUICSession, error) {
	tlsConfig, err := directTLSConfig(tlsConfig, false)
	if err != nil {
		return nil, err
	}
	conn, err := quic.DialAddr(ctx, address, tlsConfig, DirectQUICConfig(quicConfig))
	if err != nil {
		return nil, fmt.Errorf("direct QUIC dial failed: %w", err)
	}
	session := NewQUICSession(conn)
	SetPeerCapabilities(session, []string{protocol.UDPModeDatagram})
	return session, nil
}
